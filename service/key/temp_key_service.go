/*
 * @Author: <% .AuthorName %> <% .AuthorEmail %>
 * @Date: <% nowFmt "2006-01-02" %> 00:00:00
 * @LastEditors: <% .AuthorName %> <% .AuthorEmail %>
 * @LastEditTime: <% nowFmt "2006-01-02" %> 00:00:00
 * @FilePath: \<% .ProjectName %>\service\key\temp_key_service.go
 * @Description: 临时密钥服务，实现协议中心的 TempKeyService
 *
 * Copyright (c) <% .Year %> by <% .AuthorName %>, All Rights Reserved.
 */
package key

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/<% .OrgName %>/<% .ProjectName %>/constants"
	"github.com/<% .OrgName %>/<% .ProjectName %>/models"
	"github.com/<% .OrgName %>/<% .ProjectName %>/repository"
	"github.com/<% .OrgName %>/<% .ProjectName %>/service/common"
	pb "<% .ShareProtoModule %>/pb/key"
	enumspb "<% .ShareProtoModule %>/pb/enums"
	goconfig "github.com/kamalyes/go-config"
	gwglobal "github.com/kamalyes/go-rpc-gateway/global"
	"github.com/kamalyes/go-toolbox/pkg/sign"
)

// TempKeyService 实现协议中心的 TempKeyServiceServer
type TempKeyService struct {
	pb.UnimplementedTempKeyServiceServer

	repo *repository.TempKeyRepository
	pool *keyPools
}

// NewTempKeyService 构造服务
//
// 池在构造时注册并开始冷启动预填，所以这个调用会阻塞约一秒（见 registerKeyPools
// 里 coldStart 的说明）。放在 bootstrap 的服务初始化阶段，不要延迟到首个请求——
// 否则第一个用户承担全部预填成本。
//
// [EN] The pool is registered and cold-started here, so this call blocks for about a second.
// It belongs in bootstrap's service initialization rather than being deferred to the first
// request, which would make the first user pay the whole pre-fill cost.
func NewTempKeyService(repo *repository.TempKeyRepository) *TempKeyService {
	return &TempKeyService{repo: repo, pool: registerKeyPools()}
}

// Close 释放池的后台 goroutine
func (s *TempKeyService) Close() {
	if s.pool != nil {
		s.pool.Stop()
	}
}

// TempKeyIssue 签发临时密钥
func (s *TempKeyService) TempKeyIssue(
	ctx context.Context, req *pb.TempKeyIssueRequest,
) (*pb.TempKeyIssueResponse, error) {
	tenantID, err := common.TenantOf(ctx)
	if err != nil {
		return nil, err
	}

	// 场景的必填由协议的 validate 标签保证，这里只处理 UNSPECIFIED 这个
	// 「传了但传的是零值」的情形——proto3 的零值与未传无法区分，
	// validate:"required" 对枚举零值是拦得住的，但本服务也可能被
	// 非网关路径调用（服务间直连不过 HTTP 校验层），所以守一道
	// [EN] The required tag covers the enum's zero value, but this service can also be called
	// directly between services, bypassing the HTTP validation layer, so the check stays
	if req.GetScenario() == enumspb.KeyScenario_KEY_SCENARIO_UNSPECIFIED {
		return nil, common.InvalidParam(ctx)
	}

	keySize := models.RsaKeySizeToSign(req.GetKeySize())

	// 先从预生成池取；取不到才实时生成
	// [EN] Take from the pre-generated pool first and generate live only on a miss
	pem, fromPool := s.pool.Get(keySize)
	if !fromPool {
		pem, err = generateTempKeyPem(keySize)
		if err != nil {
			gwglobal.LOGGER.ErrorContextKV(ctx, "生成 RSA 密钥对失败",
				"key_size", int(keySize), "error", err)
			return nil, common.Internal(ctx)
		}
	}

	keyID := models.NewKeyID()
	expiresAt := time.Now().Add(constants.TTLTempKey)

	entry := &models.TempKeyEntry{
		PrivateKeyPem: pem.PrivateKeyPem,
		TenantID:      tenantID,
		Scenario:      req.GetScenario(),
		CreatedAt:     time.Now(),
		ExpiresAt:     expiresAt,
	}
	if err := s.repo.Save(ctx, keyID, entry, constants.TTLTempKey); err != nil {
		gwglobal.LOGGER.ErrorContextKV(ctx, "写入临时密钥失败",
			"key_id", keyID, "scenario", req.GetScenario().String(), "error", err)
		return nil, common.Internal(ctx)
	}

	// 日志里带 from_pool：这是判断池是否在起作用的唯一依据。
	// 池失效（容量配小了、补充跟不上）时服务功能完全正常，只是悄悄变慢，
	// 不看这个字段发现不了。
	// [EN] from_pool is the only way to tell whether the pool is working: when it fails through
	// an undersized capacity or a top-up that cannot keep up, the service still works and
	// merely slows down quietly
	gwglobal.LOGGER.InfoContextKV(ctx, "签发临时密钥",
		"key_id", keyID, "scenario", req.GetScenario().String(),
		"key_size", int(keySize), "from_pool", fromPool)

	return &pb.TempKeyIssueResponse{
		KeyId:        keyID,
		PublicKeyPem: pem.PublicKeyPem,
		ExpiresAt:    expiresAt.Unix(),
		Scenario:     req.GetScenario(),
	}, nil
}

// TempKeyDecrypt 用临时私钥解密
func (s *TempKeyService) TempKeyDecrypt(
	ctx context.Context, req *pb.TempKeyDecryptRequest,
) (*pb.TempKeyDecryptResponse, error) {
	tenantID, err := common.TenantOf(ctx)
	if err != nil {
		return nil, err
	}

	entry, err := s.loadEntry(ctx, req.GetKeyId(), req.GetScenario(), tenantID)
	if err != nil {
		return nil, err
	}

	plaintext, err := decryptWithEntry(entry, req.GetCiphertext())
	if err != nil {
		gwglobal.LOGGER.WarnContextKV(ctx, "解密失败",
			"key_id", req.GetKeyId(), "scenario", req.GetScenario().String(), "error", err)
		return nil, common.Internal(ctx)
	}
	return &pb.TempKeyDecryptResponse{Plaintext: plaintext}, nil
}

// TempKeyDestroy 销毁临时密钥
func (s *TempKeyService) TempKeyDestroy(
	ctx context.Context, req *pb.TempKeyDestroyRequest,
) (*pb.TempKeyDestroyResponse, error) {
	// 销毁不校验租户归属，也不校验密钥是否存在
	//
	// 不校验归属：要校验就得先读出条目，而读取失败与「不是你的」会让
	// 这个接口变成一个存在性探测器。销毁是幂等的删除，让别的租户删掉一个
	// 它猜中的 keyID，后果只是那个密钥提前失效——而密钥本来就会过期，
	// 且猜中雪花 ID 的概率可以忽略。相比之下把它做成探测器的风险更大。
	//
	// [EN] Neither ownership nor existence is checked. Checking ownership would require reading
	// the entry first, which turns this endpoint into an existence probe. Destruction is an
	// idempotent delete, and letting another tenant remove a key id it guessed only expires
	// that key early, which it would do anyway, while guessing a snowflake id is negligible.
	// Making this a probe is the larger risk.
	if req.GetScenario() == enumspb.KeyScenario_KEY_SCENARIO_UNSPECIFIED {
		return nil, common.InvalidParam(ctx)
	}

	deleted, err := s.repo.Delete(ctx, req.GetScenario(), req.GetKeyId())
	if err != nil {
		if stderrors.Is(err, repository.ErrScenarioRequired) {
			return nil, common.InvalidParam(ctx)
		}
		gwglobal.LOGGER.ErrorContextKV(ctx, "销毁临时密钥失败",
			"key_id", req.GetKeyId(), "error", err)
		return nil, common.Internal(ctx)
	}

	gwglobal.LOGGER.InfoContextKV(ctx, "销毁临时密钥",
		"key_id", req.GetKeyId(), "scenario", req.GetScenario().String(), "destroyed", deleted)
	return &pb.TempKeyDestroyResponse{Destroyed: deleted}, nil
}

// TempKeyEncryptProbe 加密自检（调试用）
//
// 生产环境应当关掉：它用服务端持有的私钥推导出公钥再加密，等于把
// 「服务端愿意用这把私钥做运算」暴露成一个接口。真实加密必须由调用方
// 用 TempKeyIssue 返回的公钥在自己这边完成。
//
// 这里按 Env 直接拒绝，而不是靠部署时不暴露路由：路由配置会被改，
// 而「生产不可用」这件事写在代码里才不会随配置漂移。
//
// [EN] Should be disabled in production, where it amounts to exposing the server's willingness
// to compute with that private key; real encryption belongs on the caller's side with the
// public key from TempKeyIssue. The refusal is in the code rather than left to not exposing the
// route at deploy time, because route configuration drifts and this constraint should not.
func (s *TempKeyService) TempKeyEncryptProbe(
	ctx context.Context, req *pb.TempKeyEncryptProbeRequest,
) (*pb.TempKeyEncryptProbeResponse, error) {
	// 用 IsProductionLevel 而不是 IsProduction：前者是「生产级别及以上」，
	// 后者是精确匹配 production 这一个名字。安全开关要宽不要窄——
	// 新增一个 release / live 之类的别名或更高级别环境时，精确匹配会漏掉它，
	// 于是这个调试接口在一个真实的生产环境里悄悄可用。
	// [EN] IsProductionLevel rather than IsProduction, which matches only that one name. A
	// security switch should err wide: a new alias such as release or live, or a higher level,
	// slips past an exact match and quietly leaves this debug endpoint live in production
	if goconfig.IsProductionLevel() {
		gwglobal.LOGGER.WarnContext(ctx, "生产环境拒绝加密自检接口")
		return nil, common.NotFound(ctx)
	}

	tenantID, err := common.TenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetPlaintext() == "" {
		return nil, common.InvalidParam(ctx)
	}

	entry, err := s.loadEntry(ctx, req.GetKeyId(), req.GetScenario(), tenantID)
	if err != nil {
		return nil, err
	}

	privateKey, err := sign.ParsePrivateKey([]byte(entry.PrivateKeyPem))
	if err != nil {
		return nil, common.Internal(ctx)
	}
	ciphertext, err := sign.EncryptOAEPWithPublicKey(&privateKey.PublicKey, []byte(req.GetPlaintext()), nil)
	if err != nil {
		return nil, common.Internal(ctx)
	}
	return &pb.TempKeyEncryptProbeResponse{Ciphertext: ciphertext}, nil
}

// loadEntry 读取并校验条目：场景必传、未过期、归属本租户
//
// 三道校验的顺序是刻意的：先场景（最便宜，且键名就带场景）、再过期、最后租户。
// 租户比对放最后是因为它需要条目已经读出来；而对外统一返回 Internal，
// 所以顺序不会泄漏信息，只是省掉无用的工作。
//
// [EN] The order is deliberate: scenario first, since it is cheapest and already part of the
// key; then expiry; then tenant, which needs the entry loaded. All three return the same error
// externally, so the order leaks nothing and merely avoids wasted work.
func (s *TempKeyService) loadEntry(
	ctx context.Context, keyID string, scenario enumspb.KeyScenario, tenantID string,
) (*models.TempKeyEntry, error) {
	if scenario == enumspb.KeyScenario_KEY_SCENARIO_UNSPECIFIED {
		return nil, common.InvalidParam(ctx)
	}

	entry, err := s.repo.Get(ctx, scenario, keyID)
	if err != nil {
		switch {
		case stderrors.Is(err, repository.ErrNotFound),
			stderrors.Is(err, repository.ErrScenarioMismatch),
			stderrors.Is(err, repository.ErrScenarioRequired):
			// 这三种对外是同一个错：区分开就等于给出一个存在性/场景探测器
			// [EN] All three look the same externally, or they become an existence probe
			gwglobal.LOGGER.WarnContextKV(ctx, "临时密钥不可用",
				"key_id", keyID, "scenario", scenario.String(), "reason", err.Error())
			return nil, common.Internal(ctx)
		default:
			gwglobal.LOGGER.ErrorContextKV(ctx, "读取临时密钥失败",
				"key_id", keyID, "error", err)
			return nil, common.Internal(ctx)
		}
	}

	if entry.Expired() {
		// 过期就顺手删掉：Redis 的 TTL 正常情况下会自己清，
		// 走到这里说明 TTL 与条目里的 ExpiresAt 不一致（见 models 里的说明），
		// 主动删掉避免它在剩余 TTL 里反复被读到
		// [EN] Delete on the way out: the TTL normally reclaims it, so reaching here means the
		// TTL and the stored expiry disagree, and removing it avoids repeated reads for the rest
		// of the TTL
		_, _ = s.repo.Delete(ctx, scenario, keyID)
		return nil, common.Internal(ctx)
	}

	if entry.TenantID != "" && entry.TenantID != tenantID {
		gwglobal.LOGGER.WarnContextKV(ctx, "临时密钥不属于当前租户",
			"key_id", keyID, "owner", entry.TenantID, "requester", tenantID)
		return nil, common.Internal(ctx)
	}
	return entry, nil
}

// decryptWithEntry 用条目里的私钥做 OAEP 解密
//
// 用 OAEP 而不是 PKCS#1 v1.5：后者存在 Bleichenbacher padding oracle，
// 而这个服务恰好提供了一个「送密文进来、告诉你成不成」的接口，
// 正是那类攻击需要的 oracle。协议与实现都必须钉在 OAEP。
//
// [EN] OAEP rather than PKCS#1 v1.5, which is vulnerable to the Bleichenbacher padding oracle
// — and this service offers exactly the submit-ciphertext-learn-whether-it-worked endpoint such
// an attack needs. Both the contract and the implementation are pinned to OAEP.
func decryptWithEntry(entry *models.TempKeyEntry, ciphertext []byte) ([]byte, error) {
	privateKey, err := sign.ParsePrivateKey([]byte(entry.PrivateKeyPem))
	if err != nil {
		return nil, err
	}
	return sign.DecryptOAEPWithPrivateKey(privateKey, ciphertext, nil)
}

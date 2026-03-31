/*
 * @Author: <% .AuthorName %> <% .AuthorEmail %>
 * @Date: <% nowFmt "2006-01-02" %> 00:00:00
 * @LastEditors: <% .AuthorName %> <% .AuthorEmail %>
 * @LastEditTime: <% nowFmt "2006-01-02" %> 00:00:00
 * @FilePath: \<% .ProjectName %>\service\key\id_block_service.go
 * @Description: ID 号段分发服务，实现协议中心的 IdBlockService
 *
 * Copyright (c) <% .Year %> by <% .AuthorName %>, All Rights Reserved.
 */
package key

import (
	"context"

	"github.com/<% .OrgName %>/<% .ProjectName %>/repository"
	"github.com/<% .OrgName %>/<% .ProjectName %>/service/common"
	pb "<% .ShareProtoModule %>/pb/key"
	gwglobal "github.com/kamalyes/go-rpc-gateway/global"
)

// IdBlockService 实现协议中心的 IdBlockServiceServer
type IdBlockService struct {
	pb.UnimplementedIdBlockServiceServer

	repo *repository.IDSequenceRepository
}

// NewIdBlockService 构造服务
func NewIdBlockService(repo *repository.IDSequenceRepository) *IdBlockService {
	return &IdBlockService{repo: repo}
}

// IdBlockAllocate 申领一段 ID
//
// 命名空间带租户前缀，不直接用调用方传的字符串
//
// 为什么：命名空间决定用哪个序列。直接用调用方传的值，两个租户传了同一个
// namespace 就共用一个序列——不会重复（序列是同一个），但一方的写入量会推高
// 另一方的号，而且谁都查不出原因。加上租户前缀之后，租户之间天然隔离，
// 调用方只需要在自己租户内保证命名空间不撞。
//
// 代价是同一个租户换了 tenantID（比如环境迁移）会从新序列从头开始发号，
// 与旧号段可能重叠。所以 ID 的唯一性只在「租户 + 命名空间」内成立，
// 跨租户复用 ID 的场景不能依赖它。
//
// [EN] The namespace is prefixed with the tenant rather than used as given. The namespace
// selects the sequence, so two tenants passing the same value share one: no duplicates, since
// it is one sequence, but each tenant's volume pushes the other's numbers along with no way to
// tell why. Prefixing isolates tenants and leaves callers to avoid collisions only within their
// own. The cost is that a changed tenant id, after an environment migration say, starts a fresh
// sequence that may overlap the old block, so uniqueness holds within a tenant and namespace
// and must not be relied on across tenants.
func (s *IdBlockService) IdBlockAllocate(
	ctx context.Context, req *pb.IdBlockAllocateRequest,
) (*pb.IdBlockAllocateResponse, error) {
	tenantID, err := common.TenantOf(ctx)
	if err != nil {
		return nil, err
	}

	// 上下界由协议的 validate 标签保证（gte=1,lte=1000）。这里只守
	// 「非网关路径调用」的情形：服务间直连不过 HTTP 校验层。
	// n 为 0 或负数会让 GenerateIDs 直接报错，但报错信息对调用方无意义，
	// 不如在这里归并成参数非法。
	// [EN] The bounds come from the contract's validate tags; this guards the direct
	// service-to-service path that bypasses the HTTP validation layer, where a non-positive
	// count would surface as an unhelpful generator error
	count := int(req.GetCount())
	if count <= 0 {
		return nil, common.InvalidParam(ctx)
	}
	if req.GetNamespace() == "" {
		return nil, common.InvalidParam(ctx)
	}

	namespace := tenantID + ":" + req.GetNamespace()

	ids, err := s.repo.Allocate(ctx, namespace, count)
	if err != nil {
		gwglobal.LOGGER.ErrorContextKV(ctx, "申领 ID 号段失败",
			"namespace", namespace, "count", count, "error", err)
		return nil, common.Internal(ctx)
	}

	// 日志只记数量不记 ID 本身：一段 1000 个 ID 打进日志毫无价值，
	// 还会把日志量放大到难以检索。要排查具体号段用首尾两个即可。
	// [EN] The log records the count, not the ids: a block of a thousand is worthless in a log
	// and inflates its volume beyond searching. The first and last suffice for investigation
	first, last := "", ""
	if len(ids) > 0 {
		first, last = ids[0], ids[len(ids)-1]
	}
	gwglobal.LOGGER.InfoContextKV(ctx, "申领 ID 号段",
		"namespace", namespace, "count", len(ids), "first", first, "last", last)

	return &pb.IdBlockAllocateResponse{
		Ids:       ids,
		Namespace: req.GetNamespace(),
	}, nil
}

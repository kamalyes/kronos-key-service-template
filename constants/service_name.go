/*
 * @Author: kamalyes example@qq.com
 * @Date: 2026-10-30 00:00:00
 * @LastEditors: kamalyes example@qq.com
 * @LastEditTime: 2026-10-30 00:00:00
 * @FilePath: \kronos-key-service\constants\service_name.go
 * @Description: 服务名称常量
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package constants

// ServiceName 服务名称
//
// 同时是配置文件前缀：go-config 按 <前缀>-<环境别名>.yaml 匹配，
// bootstrap 用 WithPrefix(ServiceName) 取它，resources/ 下的文件名由同一个
// kopy 变量渲染，所以两者不可能分叉。
//
// [EN] Also the config prefix: go-config matches <prefix>-<env alias>.yaml, bootstrap passes
// ServiceName to WithPrefix and the file name under resources/ is rendered from the same kopy
// variable, so the two cannot diverge.
const ServiceName = "kronos-key-service"

/*
 * @Author: kamalyes example@qq.com
 * @Date: 2026-10-30 00:00:00
 * @LastEditors: kamalyes example@qq.com
 * @LastEditTime: 2026-10-30 00:00:00
 * @FilePath: \kronos-key-service\main.go
 * @Description: 服务入口
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package main

import (
	"log"

	"github.com/kamalyes/kronos-key-service/bootstrap"
)

func main() {
	if err := bootstrap.NewKeyServiceGateway().Run(); err != nil {
		log.Fatalf("Gateway startup failed: %v", err)
	}
}

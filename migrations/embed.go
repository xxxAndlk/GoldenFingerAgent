// Package migrations 内嵌 SQL 迁移文件，使迁移程序无需依赖当前工作目录即可执行迁移。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

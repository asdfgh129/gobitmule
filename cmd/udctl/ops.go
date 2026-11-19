// Offline release/ops commands: `udctl backup | restore | migrate`
// (docs/13 §8 升级与迁移, §11 备份与灾难恢复). Unlike the REST commands these
// run against the LOCAL data directory and REQUIRE the daemon to be stopped —
// they must therefore be dispatched before any daemon discovery happens.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/asdfgh129/ud/internal/app"
)

// opsDBPath resolves the database the offline commands operate on:
// --db > UD_DB > the daemon's default location.
func opsDBPath(dbFlag string) string {
	if dbFlag != "" {
		return dbFlag
	}
	if env := os.Getenv("UD_DB"); env != "" {
		return env
	}
	return app.DefaultDBPath()
}

func opsCommand(name string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	switch name {
	case "backup":
		fs := flag.NewFlagSet("backup", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		dbPath := fs.String("db", "", "SQLite 数据库路径（默认 UD_DB 或用户配置目录 ud/state.db）")
		cfgPath := fs.String("config", "", "随备份导出的 config.yaml 路径（默认 ./config.yaml，存在才导出）")
		if err := fs.Parse(reorderFlags(args)); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("用法: udctl backup <目标目录> [--db <路径>] [--config <文件>]")
		}
		m, err := app.BackupBundle(ctx, opsDBPath(*dbPath), *cfgPath, fs.Arg(0))
		if err != nil {
			return err
		}
		if !rawJSON {
			fmt.Printf("备份完成: %s\n", fs.Arg(0))
			fmt.Printf("  schema v%d · 数据库 %d 字节 · %d 个钥匙串引用 · app %s\n",
				m.SchemaVersion, m.Files[len(m.Files)-1].Bytes, len(m.KeychainRefs), m.AppVersion)
			for _, f := range m.Files {
				if f.Redacted {
					fmt.Printf("  %s（remoteToken 已摘除，请另行保管）\n", f.Name)
				}
			}
			if len(m.KeychainRefs) > 0 {
				fmt.Println("  凭据本体在系统钥匙串中，不随备份走；恢复前需确保这些引用仍可用。")
			}
		}
		return printOpsJSON(m)

	case "restore":
		fs := flag.NewFlagSet("restore", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		dbPath := fs.String("db", "", "要恢复到的数据库路径（默认 UD_DB 或用户配置目录 ud/state.db）")
		if err := fs.Parse(reorderFlags(args)); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("用法: udctl restore <备份目录> [--db <路径>]")
		}
		r, err := app.RestoreBundle(ctx, fs.Arg(0), opsDBPath(*dbPath))
		if err != nil {
			return err
		}
		if !rawJSON {
			fmt.Printf("恢复完成: schema v%d，重建分片状态并逐片校验 .part\n", r.SchemaVersion)
			fmt.Printf("  恢复任务 %d 个；分片总数 %d，逐片校验复用 %d\n",
				r.RecoveredTasks, r.ChunksTotal, r.ChunksVerified)
			if r.PreRestoreBackup != "" {
				fmt.Printf("  恢复前的旧库已快照到: %s\n", r.PreRestoreBackup)
			}
			if len(r.KeychainRefs) > 0 {
				fmt.Printf("  该备份引用 %d 个钥匙串凭据，缺失时相应任务将要求重新录入。\n", len(r.KeychainRefs))
			}
		}
		return printOpsJSON(r)

	case "migrate":
		fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		dbPath := fs.String("db", "", "SQLite 数据库路径（默认 UD_DB 或用户配置目录 ud/state.db）")
		from := fs.Int("from", 0, "要求当前 schema 版本恰好为 N 时才执行（docs/13 §8.4）")
		if err := fs.Parse(reorderFlags(args)); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("用法: udctl migrate [--from N] [--db <路径>]")
		}
		r, err := app.Migrate(ctx, opsDBPath(*dbPath), *from)
		if err != nil {
			return err
		}
		if !rawJSON {
			if r.AlreadyCurrent {
				fmt.Printf("已是最新 schema v%d，无需迁移。\n", r.To)
			} else {
				fmt.Printf("迁移完成: v%d → v%d\n", r.From, r.To)
				for _, a := range r.Applied {
					fmt.Println("  applied:", a)
				}
			}
		}
		return printOpsJSON(r)
	}
	return fmt.Errorf("未知命令 %s", name)
}

func printOpsJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if rawJSON {
		_, err = os.Stdout.Write(b)
		return err
	}
	fmt.Println(string(b))
	return nil
}

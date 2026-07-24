# 后端测试归档

`test/testdata/` 按生产包路径保存全部基线 `_test.go`。这些测试是同包白盒测试，
需要访问包内未导出状态，不能作为独立 Go 包直接编译。

统一通过运行器执行：

```bash
cd server
./test/run.sh ./...
```

运行器会：

1. 动态生成 Go overlay，把归档测试虚拟映射到原包路径。
2. 调用 Go 原生测试运行器执行指定范围。
3. 退出时删除临时 overlay 配置。

overlay 不会向 `cmd/` 或 `internal/` 写入测试文件，因此并发测试不会产生同名文件冲突，
失败或中断后也不会在源码目录留下临时测试。

`test/guard/guard_test.go` 是永久运行器守卫。直接执行裸 `go test ./...` 时它会失败，
避免归档测试未加载却显示成功；overlay 运行器会设置守卫标记并正常通过。

定向执行示例：

```bash
./test/run.sh ./internal/auth
./test/run.sh ./internal/httpapi -run TestAdmin
```

如果源码包中已经存在 `_test.go`，运行器会拒绝执行并立即失败。一次性测试验证完成后
必须从 `test/testdata/` 删除；只有现有基线或用户明确要求长期保留的回归测试可以继续归档。

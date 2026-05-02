.PHONY: all run tidy clean tag build build-linux

GO_SRC=main.go
GROUP_ID=

all: run

run: tidy
	go run $(GO_SRC) $(GROUP_ID)

tidy:
	go mod tidy

clean:
	rm -rf $${TMPDIR:-/tmp}/qq-bot
	rm -rf ./build/

# Windows 本地构建（保留向后兼容）
build: tidy
	go build -o ./build/main.exe main.go

# Linux 交叉编译，产物 build/app；CI 也是用同款命令
build-linux:
	mkdir -p build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/app .

# 版本发布：Tag=1.1.10 make tag 或 Tag=v1.1.10 make tag
# 未带 v 前缀会自动补全，以匹配 CI 的 v* tag 触发
tag:
ifndef Tag
	$(error 请指定 Tag，如: Tag=1.0.0 make tag)
endif
	@t="$(Tag)"; [ "$${t#v}" = "$$t" ] && t="v$$t"; \
	echo "创建并推送 tag: $$t"; \
	git tag $$t && git push origin $$t

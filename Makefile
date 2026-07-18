.PHONY: build build-linux build-cli clean run docker docker-up docker-down

APP=zyzu
CLI=zyzu-cli
CMD=./cmd/zyzu/
CMD_CLI=./cmd/zyzu-cli/
IMAGE=madtoby2/zyzu

build:
	GOPROXY=https://goproxy.cn,direct go build -ldflags="-s -w" -o $(APP).exe $(CMD)
	GOPROXY=https://goproxy.cn,direct go build -ldflags="-s -w" -o $(CLI).exe $(CMD_CLI)

build-linux:
	GOPROXY=https://goproxy.cn,direct GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(APP)_linux $(CMD)
	GOPROXY=https://goproxy.cn,direct GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(CLI)_linux $(CMD_CLI)

build-cli:
	GOPROXY=https://goproxy.cn,direct go build -ldflags="-s -w" -o $(CLI).exe $(CMD_CLI)

run:
	GOPROXY=https://goproxy.cn,direct go run $(CMD)

clean:
	rm -f $(APP).exe $(APP)_linux $(CLI).exe $(CLI)_linux

docker:
	docker build -t $(IMAGE):latest .

docker-push: docker
	docker push $(IMAGE):latest

docker-up:
	docker compose up -d

docker-down:
	docker compose down

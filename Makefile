.PHONY: build build-linux clean run docker docker-up docker-down

APP=zyzu
CMD=./cmd/zyzu/
IMAGE=madtoby2/zyzu

build:
	GOPROXY=https://goproxy.cn,direct go build -ldflags="-s -w" -o $(APP).exe $(CMD)

build-linux:
	GOPROXY=https://goproxy.cn,direct GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(APP)_linux $(CMD)

run:
	GOPROXY=https://goproxy.cn,direct go run $(CMD)

clean:
	rm -f $(APP).exe $(APP)_linux

docker:
	docker build -t $(IMAGE):latest .

docker-push: docker
	docker push $(IMAGE):latest

docker-up:
	docker compose up -d

docker-down:
	docker compose down

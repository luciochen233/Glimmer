.PHONY: build build-arm run clean hash

build:
	go build -ldflags="-s -w" -o glimmer.exe ./

build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o glimmer ./

build-arm:
	GOOS=linux GOARCH=arm GOARM=5 go build -ldflags="-s -w" -o glimmer-arm ./

run: build
	./glimmer.exe

clean:
	rm -f glimmer glimmer.exe glimmer-arm

hash:
	@read -p "Password: " pw && go run . --hash-password "$$pw"

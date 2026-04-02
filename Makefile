.PHONY: build build-arm run clean hash

build:
	go build -ldflags="-s -w" -o urlshort.exe ./

build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o urlshort ./

build-arm:
	GOOS=linux GOARCH=arm GOARM=5 go build -ldflags="-s -w" -o urlshort-arm ./

run: build
	./urlshort.exe

clean:
	rm -f urlshort urlshort.exe urlshort-arm

hash:
	@read -p "Password: " pw && go run . --hash-password "$$pw"

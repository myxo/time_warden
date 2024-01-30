build:
	go build -o usr/bin/time_warden ./...

build_linux:
	GOOS=linux GOARCH=amd64 go build -o usr/bin/time_warden ./...

run:
	go run ./... -token-file=token -categories=etc/time_warden_category.yml

deb:
	rm -rf PKG_SOURCE
	mkdir -p PKG_SOURCE
	cp -Rf ./usr ./etc ./DEBIAN PKG_SOURCE
	dpkg-deb --build PKG_SOURCE time_warden.deb

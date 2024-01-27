build:
	go build -o usr/bin/time_warden ./...

build_linux:
	GOOS=linux GOARCH=amd64 go build -o usr/bin/time_warden ./...

deb:
	rm -rf PKG_SOURCE
	mkdir -p PKG_SOURCE
	cp -Rf ./usr ./etc ./DEBIAN PKG_SOURCE
	dpkg-deb --build PKG_SOURCE time_warden.deb

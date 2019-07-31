module github.com/mumoshu/brigade-cd

go 1.12

require (
	cloud.google.com/go v0.37.4 // indirect
	github.com/brigadecore/brigade v0.0.0-20190321181924-1eec8b07ca78
	github.com/dgrijalva/jwt-go v3.2.0+incompatible
	github.com/gin-contrib/sse v0.0.0-20190301062529-5545eab6dad3 // indirect
	github.com/gin-gonic/gin v1.3.0 // indirect
	github.com/google/go-github/v27 v27.0.4
	github.com/mattn/go-isatty v0.0.7 // indirect
	github.com/oklog/ulid v1.3.1 // indirect
	github.com/prometheus/client_golang v1.0.0 // indirect
	github.com/summerwind/whitebox-controller v0.7.0
	github.com/ugorji/go/codec v0.0.0-20181204163529-d75b2dcb6bc8 // indirect
	golang.org/x/crypto v0.0.0-20190701094942-4def268fd1a4 // indirect
	golang.org/x/oauth2 v0.0.0-20190402181905-9f3314589c9a
	gopkg.in/gin-gonic/gin.v1 v1.0.0-20170702092826-d459835d2b07
	gopkg.in/go-playground/assert.v1 v1.2.1 // indirect
	gopkg.in/go-playground/validator.v8 v8.18.2 // indirect
	k8s.io/api v0.0.0-20190409021203-6e4e0e4f393b
	k8s.io/apimachinery v0.0.0-20190612205821-1799e75a0719
	k8s.io/client-go v11.0.1-0.20190409021438-1a26190bd76a+incompatible
	k8s.io/kube-openapi v0.0.0-20190722073852-5e22f3d471e6 // indirect
	sigs.k8s.io/controller-runtime v0.2.0-beta.4
)

replace github.com/docker/distribution => github.com/docker/distribution v2.7.1+incompatible

replace k8s.io/api => k8s.io/api v0.0.0-20190704095032-f4ca3d3bdf1d

replace k8s.io/apimachinery => k8s.io/apimachinery v0.0.0-20190704094733-8f6ac2502e51

replace k8s.io/client-go => k8s.io/client-go v11.0.1-0.20190704100234-640d9f240853+incompatible

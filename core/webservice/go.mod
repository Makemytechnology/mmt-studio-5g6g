// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
module github.com/mmt/mmt-studio-core/webservice

go 1.21

require (
	github.com/mmt/mmt-studio-core/db v0.0.0
	github.com/mmt/mmt-studio-core/infra v0.0.0
	github.com/mmt/mmt-studio-core/nf v0.0.0
	github.com/mmt/mmt-studio-core/oam v0.0.0
	github.com/mmt/mmt-studio-core/security v0.0.0
	github.com/mmt/mmt-studio-core/services/ims v0.0.0
	github.com/mmt/mmt-studio-core/services/nsaas v0.0.0
	github.com/mmt/mmt-studio-core/services/supplementary v0.0.0
	github.com/mmt/mmt-studio-core/services/ussd v0.0.0
	github.com/flosch/pongo2/v6 v6.0.0
	github.com/go-chi/chi/v5 v5.1.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/mmt/asn1go v0.0.0 // indirect
	github.com/mmt/mmt-studio-core/libs/fsm v0.0.0 // indirect
	github.com/mmt/mmt-studio-core/libs/sacrypto v0.0.0 // indirect
	github.com/mmt/mmt-studio-core/libs/sip v0.0.0 // indirect
	github.com/mmt/nasgen v0.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.22.0 // indirect
	modernc.org/gc/v3 v3.0.0-20240107210532-573471604cb6 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
	modernc.org/sqlite v1.34.1 // indirect
	modernc.org/strutil v1.2.0 // indirect
	modernc.org/token v1.1.0 // indirect
)

replace (
	github.com/mmt/asn1go => ../codecs/asn1-go
	github.com/mmt/mmt-studio-core/db => ../db
	github.com/mmt/mmt-studio-core/infra => ../infra
	github.com/mmt/mmt-studio-core/libs/sacrypto => ../libs/sacrypto
	github.com/mmt/mmt-studio-core/nf => ../nf
	github.com/mmt/mmt-studio-core/libs/fsm => ../libs/fsm
	github.com/mmt/mmt-studio-core/libs/sip => ../libs/sip
	github.com/mmt/mmt-studio-core/oam => ../oam
	github.com/mmt/mmt-studio-core/security => ../security
	github.com/mmt/mmt-studio-core/services/ims => ../services/ims
	github.com/mmt/mmt-studio-core/services/nsaas => ../services/nsaas
	github.com/mmt/mmt-studio-core/services/supplementary => ../services/supplementary
	github.com/mmt/mmt-studio-core/services/ussd => ../services/ussd
	github.com/mmt/nasgen => ../codecs/tlv-3gpp-nas/nasgen
)

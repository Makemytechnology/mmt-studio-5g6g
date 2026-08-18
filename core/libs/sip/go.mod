// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
module github.com/mmt/mmt-studio-core/libs/sip

go 1.21

require (
	github.com/google/uuid v1.6.0
	github.com/mmt/mmt-studio-core/libs/fsm v0.0.0
	github.com/mmt/mmt-studio-core/oam v0.0.0
)

replace (
	github.com/mmt/mmt-studio-core/libs/fsm => ../fsm
	github.com/mmt/mmt-studio-core/oam => ../../oam
)

module github.com/fleamarket/cmd/dbctl

go 1.26.0

replace (
	github.com/fleamarket/api => ../../api
	github.com/fleamarket/app/product => ../../app/product
	github.com/fleamarket/app/review => ../../app/review
	github.com/fleamarket/app/trade => ../../app/trade
	github.com/fleamarket/app/user => ../../app/user
	github.com/fleamarket/pkg => ../../pkg
)

require (
	github.com/fleamarket/app/product v0.0.0-00010101000000-000000000000
	github.com/fleamarket/app/review v0.0.0-00010101000000-000000000000
	github.com/fleamarket/app/trade v0.0.0-00010101000000-000000000000
	github.com/fleamarket/app/user v0.0.0-00010101000000-000000000000
	github.com/fleamarket/pkg v0.0.0-00010101000000-000000000000
	gorm.io/gorm v1.31.2
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/go-sql-driver/mysql v1.10.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	golang.org/x/text v0.41.0 // indirect
	gorm.io/driver/mysql v1.6.0 // indirect
)

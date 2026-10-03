module github.com/bornholm/indecis-caviardage

go 1.27

require (
	github.com/bornholm/go-anon v0.0.0
	github.com/bornholm/indecis v0.3.0
	github.com/bornholm/indecis-ner v0.0.0
)

require github.com/gomutex/godocx v0.1.5 // indirect

replace (
	github.com/bornholm/go-anon => ../go-anon
	github.com/bornholm/indecis => ../indecis
	github.com/bornholm/indecis-ner => ../indecis-ner
)

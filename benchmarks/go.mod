module github.com/apsis-io/velocity/benchmarks

go 1.27

replace github.com/apsis-io/velocity => ../

require (
	github.com/aaronjan/hunch v1.1.3
	github.com/apsis-io/velocity v0.0.0
	github.com/samber/go-singleflightx v0.3.2
	golang.org/x/sync v0.22.0
	resenje.org/singleflight v0.4.3
)

require github.com/puzpuzpuz/xsync/v4 v4.5.0 // indirect

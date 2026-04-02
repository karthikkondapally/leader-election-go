module github.com/pgelect/pgelect

go 1.22

require (
	// Optional: only needed if you import github.com/pgelect/pgelect/pgxadapter
	github.com/jackc/pgx/v5 v5.7.2

	// Optional: only needed if you import github.com/pgelect/pgelect/gormadapter
	gorm.io/gorm v1.25.12

	// Optional: only needed if you import github.com/pgelect/pgelect/fxpgelect
	go.uber.org/fx v1.23.0
)

// The core package (github.com/pgelect/pgelect) uses ONLY the standard library.
// The sub-packages (fxpgelect, pgxadapter, gormadapter) each pull one extra dep.
// Go's lazy loading means you only compile what you import.

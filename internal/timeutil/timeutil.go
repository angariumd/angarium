package timeutil

import "time"

const SQLLayout = "2006-01-02 15:04:05"

func Format(t time.Time) string {
	return t.UTC().Format(SQLLayout)
}

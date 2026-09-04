package domain

// The IANA zone database is embedded rather than read from the host. Quiet hours are the
// one rule in this package that depends on where the recipient is, and the answer must not
// depend on what the machine running the worker happens to have installed — Windows ships
// no zoneinfo at all, and a member in Istanbul would then be told at four in the morning
// on one host and not on another. Embedding costs about 450 KB in the binary and makes
// InQuietHours give the same answer everywhere, including in tests.
//
// time.LoadLocation still prefers the host database when there is one, so an operating
// system with newer zone rules than this build wins; the embedded copy is the fallback.
import _ "time/tzdata"

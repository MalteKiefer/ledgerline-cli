// Package authflow drives the two ways this client can obtain a credential, so
// that the terminal and the desktop GUI share one implementation and one set of
// error messages.
//
//   - Password: e-mail + password, plus the account's second factor when it has
//     one. The server decides: it answers 422 {two_factor:true} until a valid
//     TOTP or recovery code arrives, so the factor cannot be skipped by talking
//     to the API directly instead of the web app.
//   - Pair: a one-time code generated in the web profile and approved there.
//     Nothing but that code crosses the boundary; the password is never typed
//     into this program at all.
//
// Both end the same way: verify the issued token against /me before storing it
// (a credential that cannot call /me is worse than none, because every later
// command would fail for a reason the user cannot see), then save the session.
package authflow

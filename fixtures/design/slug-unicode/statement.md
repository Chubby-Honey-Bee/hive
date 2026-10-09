# Implement Slugify for Unicode titles

Package `slug` (file `slug.go`) exports `func Slugify(s string) string`. Implement it. The signature and the package name stay as they are.

The contract:

- Letters and digits of any script are kept, as `unicode.IsLetter` and `unicode.IsDigit` define them, each lowercased with `unicode.ToLower`.
- An apostrophe (`'`) between two kept characters is dropped and produces no hyphen: `don't stop` gives `dont-stop`.
- Every other maximal run of characters becomes one hyphen. Leading and trailing hyphens are removed.
- There is no length limit.
- When nothing is kept, the result is `untitled`.
- Slugify of a slug is the same slug.

Examples: `Hello, World!` gives `hello-world`; `Ünïcode Title` gives `ünïcode-title`; `日本語 テスト` gives `日本語-テスト`; `!!!` gives `untitled`.

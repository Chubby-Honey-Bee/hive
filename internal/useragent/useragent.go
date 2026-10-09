// Package useragent builds the User-Agent that every outbound HTTP request
// from chb and chb-mcp carries, so third-party servers see one name.
package useragent

// Version is the release version the running binary reports. Each binary's
// main sets it from its own -X main.version at startup; it is "dev" otherwise.
var Version = "dev"

// homepage is the project page a server operator can follow from the header.
const homepage = "https://github.com/Chubby-Honey-Bee/hive"

// For returns "chb/<Version> (+<homepage>) <role>". The role names the chb
// command that makes the request, such as validate-sources.
func For(role string) string {
	return "chb/" + Version + " (+" + homepage + ") " + role
}

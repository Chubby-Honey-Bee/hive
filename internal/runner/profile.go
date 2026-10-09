package runner

import (
	"bytes"
	"cmp"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"gopkg.in/yaml.v3"
)

// Profile is a routing profile from the models config, checked and resolved
// for a run: every route has a provider kind and a parsed TTL.
type Profile struct {
	Name    string
	Quality string
	// Provider is the profile's provider: the provider of a route that
	// names none, and the run default under the profile.
	Provider BackendKind
	// Routes are the routes by role.
	Routes map[string]Route
	// Default is the route of a model node that no role route serves and
	// that names no model and no provider of its own (takesDefault); nil
	// when the profile sets none.
	Default *Route
}

// Route is one role's routing in a resolved profile.
type Route struct {
	Role      string
	Model     string
	Provider  BackendKind
	Reasoning string
	// Tools is nil when the route keeps the node's tools.
	Tools *[]string
	// TTL is 0 when the route keeps the node's.
	TTL     time.Duration
	Because string
}

// Cloud reports whether the route leaves this machine by what it declares:
// any provider but local, or an Ollama cloud model (ollamaCloudModel).
func (r Route) Cloud() bool {
	return r.Provider != BackendLocal || ollamaCloudModel(r.Model)
}

// CloudRoles are the profile's roles whose routes are Cloud, sorted.
func (p *Profile) CloudRoles() []string {
	var out []string
	for role, r := range p.Routes {
		if r.Cloud() {
			out = append(out, role)
		}
	}
	sort.Strings(out)
	return out
}

// ollamaCloudModel reports whether model names an Ollama cloud model, which
// a local Ollama forwards to ollama.com: a tag of `cloud` or one ending in
// `-cloud`, such as gpt-oss:120b-cloud. That every cloud model is tagged so
// is a bet on Ollama's naming.
func ollamaCloudModel(model string) bool {
	return models.OllamaCloudModel(model)
}

// ResolveProfile reads the named profile from the models config and checks
// it. Every route needs a model and a provider (its own, else the
// profile's) that is a known kind; a reasoning level of
// models.ReasoningLevels; tools of workflow.ToolNames, by an alias of
// workflow.ToolAliases or not; a TTL that is a
// positive Go duration; a role of models.Roles; and no key outside
// models.RouteKeys, as the profile none outside models.ProfileKeys. A route
// that leaves this machine (Route.Cloud) must say why in `because:`, the
// evidence that no local model is sufficient for the role; that it is there
// is checked, not what it says. The default route is checked as a role's
// route is, and may not leave this machine at all. A repair route
// (models.RepairRoles) runs on its node's provider with its node's
// reasoning, tools and TTL, so it sets a model and at most the same provider
// as implement-fix. Every problem is named in one error.
func ResolveProfile(name string) (*Profile, error) {
	return resolveProfileFrom(models.Load(), name)
}

// resolveProfileFrom is ResolveProfile on a given models config.
func resolveProfileFrom(cfg *models.Config, name string) (*Profile, error) {
	mp, err := cfg.Profile(name)
	if err != nil {
		return nil, err
	}
	c := &profileCheck{mp: mp}
	c.profileKeys()
	provider := c.provider()
	routes := c.routes()
	def := c.defaultRoute()
	if len(c.problems) > 0 {
		return nil, fmt.Errorf("routing profile %s: %s", name, strings.Join(c.problems, "; "))
	}
	return &Profile{Name: name, Quality: mp.Quality, Provider: provider, Routes: routes, Default: def}, nil
}

// profileCheck gathers the problems ResolveProfile finds in mp, a profile
// as the models config holds it.
type profileCheck struct {
	mp       models.Profile
	problems []string
}

// profileKeys notes each key the profile sets that is none of
// models.ProfileKeys.
func (c *profileCheck) profileKeys() {
	for _, k := range c.mp.Unknown {
		c.problems = append(c.problems, fmt.Sprintf("unknown key %q (known: %s)", k, strings.Join(models.ProfileKeys, ", ")))
	}
}

// provider is the kind of the profile's provider, "" when it names none or
// one that is not known, which is a problem.
func (c *profileCheck) provider() BackendKind {
	kind := canonicalKind(c.mp.Provider)
	if c.mp.Provider != "" && kind == "" {
		c.problems = append(c.problems, fmt.Sprintf("provider %q is not a known provider", c.mp.Provider))
	}
	return kind
}

// routes are the profile's routes by role, checked in role order.
func (c *profileCheck) routes() map[string]Route {
	routes := map[string]Route{}
	for _, role := range slices.Sorted(maps.Keys(c.mp.Roles)) {
		if route, ok := c.route(role); ok {
			routes[role] = route
		}
	}
	return routes
}

// route is role's route, its problems noted; ok is false for a role that is
// none of models.Roles.
func (c *profileCheck) route(role string) (Route, bool) {
	rc := routeCheck{profileCheck: c, role: role, r: c.mp.Roles[role]}
	if !slices.Contains(models.Roles, role) {
		rc.bad("not a role (known: %s)", strings.Join(models.Roles, ", "))
		return Route{}, false
	}
	return rc.check(), true
}

// defaultRoute is the profile's default route, checked as a role's route is
// (routeCheck); nil when the profile sets none.
func (c *profileCheck) defaultRoute() *Route {
	if c.mp.Default == nil {
		return nil
	}
	route := routeCheck{profileCheck: c, r: *c.mp.Default}.check()
	return &route
}

// routeCheck checks r, role's route in the profile, or its default route
// when role is "".
type routeCheck struct {
	*profileCheck
	role string
	r    models.Route
}

// bad notes a problem with the route.
func (rc routeCheck) bad(f string, a ...any) {
	rc.problems = append(rc.problems, rc.name()+": "+fmt.Sprintf(f, a...))
}

// name names the route in a problem: its role, or the default route.
func (rc routeCheck) name() string {
	if rc.role == "" {
		return "default route"
	}
	return "role " + rc.role
}

// check is the role's route, each of its problems noted in turn: unknown
// keys, no model, its provider, its reasoning, its tools, its ttl, a cloud
// route with no because:, and what a repair route may not set.
func (rc routeCheck) check() Route {
	r := rc.r
	for _, k := range r.Unknown {
		rc.bad("unknown key %q (known: %s)", k, strings.Join(models.RouteKeys, ", "))
	}
	route := Route{Role: rc.role, Model: strings.TrimSpace(r.Model), Reasoning: r.Reasoning, Tools: r.Tools, Because: strings.TrimSpace(r.Because)}
	if route.Model == "" {
		rc.bad("names no model")
	}
	route.Provider = rc.provider()
	rc.checkReasoning()
	rc.checkTools()
	route.TTL = rc.ttl()
	rc.checkCloud(route)
	rc.checkRepair(route)
	return route
}

// provider is the kind of the route's provider, its own else the
// profile's; "" when it names none or one that is not known.
func (rc routeCheck) provider() BackendKind {
	switch prov := firstNonEmptyString(rc.r.Provider, rc.mp.Provider); {
	case prov == "":
		rc.bad("names no provider, and the profile has no provider:")
	case canonicalKind(prov) == "":
		rc.bad("provider %q is not a known provider", prov)
	default:
		return canonicalKind(prov)
	}
	return ""
}

// checkReasoning notes a reasoning level the route sets that is none of
// models.ReasoningLevels.
func (rc routeCheck) checkReasoning() {
	if rc.r.Reasoning == "" {
		return
	}
	if err := models.CheckReasoningLevel(rc.r.Reasoning); err != nil {
		rc.bad("%v", err)
	}
}

// checkTools notes each tool the route names that is none of
// workflow.ToolNames, by an alias or not.
func (rc routeCheck) checkTools() {
	if rc.r.Tools == nil {
		return
	}
	for _, t := range *rc.r.Tools {
		if !slices.Contains(workflow.ToolNames, workflow.CanonicalTool(t)) {
			rc.bad("tools names unknown tool %q (known: %s)", t, strings.Join(workflow.ToolNames, ", "))
		}
	}
}

// ttl is the route's TTL, 0 when it sets none; one that is not a positive
// duration is a problem.
func (rc routeCheck) ttl() time.Duration {
	if rc.r.TTL == "" {
		return 0
	}
	d, err := time.ParseDuration(strings.TrimSpace(rc.r.TTL))
	if err != nil || d <= 0 {
		rc.bad("ttl %q is not a positive duration such as 5m", rc.r.TTL)
	}
	return d
}

// checkCloud notes a route that leaves this machine (Route.Cloud) when it
// may not (cloudRefusal).
func (rc routeCheck) checkCloud(route Route) {
	if route.Provider == "" || !route.Cloud() {
		return
	}
	if why := rc.cloudRefusal(route); why != "" {
		rc.bad("routes %s on %s, a cloud route: %s; %s", route.Model, route.Provider, cloudReason(route), why)
	}
}

// cloudRefusal is why route, a cloud route, is refused: the default route
// stays on this machine, and a role's route says why it leaves. "" for a
// role's route that says why.
func (rc routeCheck) cloudRefusal(route Route) string {
	switch {
	case rc.role == "":
		return "the default route stays on this machine, since calls leave it only on a role's cloud route, with its because:"
	case route.Because == "":
		return "it says no because:, so name the measured evidence (a Bench-1 report) that no local model is sufficient for the role"
	}
	return ""
}

// cloudReason says why route, a cloud route, leaves this machine.
func cloudReason(route Route) string {
	if route.Provider == BackendLocal {
		return "an Ollama cloud model, which Ollama forwards to ollama.com"
	}
	return fmt.Sprintf("provider %s, whatever endpoint serves it, since only provider local counts as this machine (a server on it, such as Ollama, is provider: local at HIVE_LOCAL_BASE_URL)", route.Provider)
}

// checkRepair holds a repair route (models.RepairRoles) to what a repair
// takes: it runs on its node's provider with its node's reasoning, tools and
// TTL, so it sets a model and at most the same provider as implement-fix.
func (rc routeCheck) checkRepair(route Route) {
	if !slices.Contains(models.RepairRoles, rc.role) {
		return
	}
	if rc.setsNodeFields() {
		rc.bad("a repair runs with its node's reasoning, tools and ttl, so its route sets a model and a provider only")
	}
	if !rc.onFixProvider(route.Provider) {
		rc.bad("a repair runs on its node's provider, so its provider must be implement-fix's")
	}
}

// setsNodeFields reports whether the route sets a reasoning level, tools or
// a TTL.
func (rc routeCheck) setsNodeFields() bool {
	return rc.r.Reasoning != "" || rc.r.Tools != nil || rc.r.TTL != ""
}

// onFixProvider reports whether provider is implement-fix's, its own else
// the profile's; true when the profile routes no implement-fix.
func (rc routeCheck) onFixProvider(provider BackendKind) bool {
	fix, ok := rc.mp.Roles["implement-fix"]
	return !ok || canonicalKind(firstNonEmptyString(fix.Provider, rc.mp.Provider)) == provider
}

// ApplyProfile writes p's routes onto the workflow text: each node whose
// `role:` the profile routes gets the route's model (its `tier:` removed),
// provider, and the reasoning, tools and ttl the route sets; what a route
// leaves out stays as the node declares it. A model node that no role route
// serves and that names no model and no provider of its own gets the
// profile's default route the same way, when the profile sets one. The
// node's `on_reject:` block, when it names a role the profile routes, gets
// that route's model; one that names no role loses its model and tier, so
// the repair runs on the node's routed model. A node the profile does not
// route is left as written. Keys keep their order, so an output_schema keeps
// its declared property order. The result must pass the engine's node
// checks, or the error says what the profile made of which node.
func ApplyProfile(text string, p *Profile) (string, error) {
	defn, doc, err := parseWorkflowDoc(text)
	if err != nil {
		return "", err
	}
	nodes := mappingAt(doc.Content[0], "nodes")
	if nodes == nil {
		return text, nil
	}
	decoded, _ := defn["nodes"].(map[string]any)
	if err := p.routeNodes(nodes, decoded); err != nil {
		return "", err
	}
	return p.encodeRouted(doc)
}

// parseWorkflowDoc decodes a workflow's text twice: as the engine reads it,
// and as a YAML node tree whose edits keep its keys' order.
func parseWorkflowDoc(text string) (map[string]any, *yaml.Node, error) {
	defn, err := workflow.LoadYAMLString(text)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil, fmt.Errorf("the workflow is empty")
	}
	return defn, &doc, nil
}

// routeNodes writes p's routes onto the nodes of nodes, the workflow's nodes
// mapping; decoded holds the same nodes as the engine reads them.
func (p *Profile) routeNodes(nodes *yaml.Node, decoded map[string]any) error {
	for i := 0; i+1 < len(nodes.Content); i += 2 {
		name := nodes.Content[i].Value
		node, _ := decoded[name].(map[string]any)
		if err := p.routeNode(name, nodes.Content[i+1], node); err != nil {
			return err
		}
	}
	return nil
}

// routeNode writes the route node takes (nodeRoute) onto n, node name's
// YAML, when it takes one.
func (p *Profile) routeNode(name string, n *yaml.Node, node map[string]any) error {
	route, ok := p.nodeRoute(node)
	if !ok {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("routing profile %s: node %q is an alias of another node, so it cannot be routed on it alone", p.Name, name)
	}
	setRoute(n, route)
	return p.routeRepair(name, n, node)
}

// nodeRoute is the route node takes: its role's, when the profile routes
// that role, else the default route (defaultRouteFor). ok is false for a
// node that takes none.
func (p *Profile) nodeRoute(node map[string]any) (Route, bool) {
	role, _ := node["role"].(string)
	if route, ok := p.Routes[role]; ok && role != "" {
		return route, true
	}
	return p.defaultRouteFor(node)
}

// defaultRouteFor is the profile's default route, for a node that takes it
// (takesDefault); ok is false for any other node, and when the profile sets
// no default route.
func (p *Profile) defaultRouteFor(node map[string]any) (Route, bool) {
	if p.Default == nil || !takesDefault(node) {
		return Route{}, false
	}
	return *p.Default, true
}

// takesDefault reports whether node leaves where it runs to the profile: a
// backend serves it (sentToBackend), and it names no model and no provider of
// its own.
func takesDefault(node map[string]any) bool {
	model, _ := node["model"].(string)
	provider, _ := node["provider"].(string)
	return sentToBackend(node) && model == "" && provider == ""
}

// setRoute writes route onto n, a node: its model, in place of any tier, and
// its provider, and the reasoning, tools and ttl it sets.
func setRoute(n *yaml.Node, route Route) {
	setScalar(n, "model", route.Model)
	deleteKey(n, "tier")
	setScalar(n, "provider", string(route.Provider))
	if route.Reasoning != "" {
		setScalar(n, "reasoning", route.Reasoning)
	}
	if route.Tools != nil {
		setList(n, "tools", *route.Tools)
	}
	if route.TTL > 0 {
		setScalar(n, "ttl", route.TTL.String())
	}
}

// routeRepair routes the on_reject block of node name, n in YAML, when it
// names a model, a tier or a role (setRepairRoute); one that names none
// repairs on the routed model as it is.
func (p *Profile) routeRepair(name string, n *yaml.Node, node map[string]any) error {
	block, _ := node["on_reject"].(map[string]any)
	if !namesRepairModel(block) {
		return nil
	}
	b := mappingAt(n, "on_reject")
	if b == nil {
		return fmt.Errorf("routing profile %s: node %q takes its on_reject: from a merge key or an alias, so its repair cannot be routed", p.Name, name)
	}
	brole, _ := block["role"].(string)
	p.setRepairRoute(b, brole)
	return nil
}

// namesRepairModel reports whether an on_reject block names a model, a tier
// or a role.
func namesRepairModel(block map[string]any) bool {
	_, names := block["model"]
	_, tiers := block["tier"]
	_, roles := block["role"]
	return names || tiers || roles
}

// setRepairRoute writes the repair's model onto b, an on_reject block that
// names role brole: the model of that role's route, in place of any tier,
// when the profile routes it; no model and no tier when the block names no
// role.
func (p *Profile) setRepairRoute(b *yaml.Node, brole string) {
	if repair, ok := p.Routes[brole]; ok && brole != "" {
		setScalar(b, "model", repair.Model)
		deleteKey(b, "tier")
	} else if brole == "" {
		deleteKey(b, "model")
		deleteKey(b, "tier")
	}
}

// encodeRouted is doc, the routed workflow, as text, once it passes the
// engine's node checks.
func (p *Profile) encodeRouted(doc *yaml.Node) (string, error) {
	out, err := encodeYAML(doc)
	if err != nil {
		return "", err
	}
	if err := checkRoutedWorkflow(out); err != nil {
		return "", fmt.Errorf("routing profile %s on this workflow: %w", p.Name, err)
	}
	return out, nil
}

// encodeYAML is doc as YAML text, indented by two spaces.
func encodeYAML(doc *yaml.Node) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// checkRoutedWorkflow runs the engine's node checks on out, a routed
// workflow's text.
func checkRoutedWorkflow(out string) error {
	routed, err := workflow.LoadYAMLString(out)
	if err != nil {
		return err
	}
	if err := workflow.CheckNodeFields(routed); err != nil {
		return err
	}
	return workflow.CheckReasoning(routed)
}

// mappingValue is the value node of key in mapping m, written on m itself;
// nil when m has no such key.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	if i := keyIndex(m, key); i >= 0 {
		return m.Content[i+1]
	}
	return nil
}

// mappingAt is the value of key in mapping m when that is a mapping itself,
// nil otherwise.
func mappingAt(m *yaml.Node, key string) *yaml.Node {
	if v := mappingValue(m, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	return nil
}

// keyIndex is where key stands in mapping m's content, -1 when m does not
// hold it itself.
func keyIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// setScalar sets key to the string value in mapping m, replacing the value
// in place or appending the key.
func setScalar(m *yaml.Node, key, value string) {
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	setNode(m, key, v)
}

// setList sets key to a flow list of the strings in mapping m.
func setList(m *yaml.Node, key string, items []string) {
	v := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, s := range items {
		v.Content = append(v.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s})
	}
	setNode(m, key, v)
}

func setNode(m *yaml.Node, key string, v *yaml.Node) {
	if i := keyIndex(m, key); i >= 0 {
		m.Content[i+1] = v
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

// deleteKey removes key from mapping m, when m holds it itself.
func deleteKey(m *yaml.Node, key string) {
	if i := keyIndex(m, key); i >= 0 {
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
	}
}

// RouteLine is where one group of a workflow's model calls goes: the nodes
// of one role, or one node with no role, that share a provider, model,
// endpoint and reasoning level.
type RouteLine struct {
	Role      string // "" for nodes that name none
	Nodes     []string
	Provider  BackendKind
	Model     string // as sent; "" leaves the provider's default
	Endpoint  string // "" where chb does not choose the host
	Reasoning string
	// OffMachine says why these calls leave this machine; "" when they
	// stay on it.
	OffMachine string
}

// Routing is where each model call of the workflow goes: one line per role
// (or per node with no role) and target, the repairs whose block names its
// own model among them as "<node> on_reject". Nodes resolve as dispatch
// resolves them (resolveDispatchParams), under the run's provider and
// budget mode. Sorted by role, then model.
func Routing(cfg Config, defn map[string]any) []RouteLine {
	groups := routeGroups{}
	forEachAgentNode(defn, runDefaultKind(cfg), func(name string, node map[string]any, kind BackendKind) {
		groups.addNode(cfg, name, node, kind)
	})
	return groups.lines()
}

// routeKey is what the calls of one RouteLine share.
type routeKey struct{ role, provider, model, endpoint, reasoning string }

// routeGroups gathers a workflow's model calls into RouteLines.
type routeGroups map[routeKey]*RouteLine

// addNode adds the calls of node name, served on kind: its own, and its
// repair's.
func (g routeGroups) addNode(cfg Config, name string, node map[string]any, kind BackendKind) {
	role, _ := node["role"].(string)
	model, _ := node["model"].(string)
	tier, _ := node["tier"].(string)
	reasoning, _ := node["reasoning"].(string)
	g.add(role, name, kind, nodeModel(model, tier, cfg.BudgetMode, kind), reasoning)
	g.addRepair(cfg, name, node, kind, role, reasoning)
}

// addRepair adds the call of node name's repair, when its on_reject block
// runs a repair on a model of its own, under the block's role, else role,
// the node's.
func (g routeGroups) addRepair(cfg Config, name string, node map[string]any, kind BackendKind, role, reasoning string) {
	block, _ := node["on_reject"].(map[string]any)
	if block == nil || intFromMap(block, "max_repair_iterations", 3) <= 0 {
		return
	}
	rm := repairModelFor(block, cfg.BudgetMode, kind)
	if rm == "" {
		return
	}
	brole, _ := block["role"].(string)
	g.add(cmp.Or(brole, role), name+" on_reject", kind, resolveModelForProvider(kind, rm), reasoning)
}

// add adds node's call, model on kind for role at reasoning, to its line.
func (g routeGroups) add(role, node string, kind BackendKind, model, reasoning string) {
	endpoint := endpointFor(kind)
	k := routeKey{role, string(kind), model, endpoint, reasoning}
	line := g[k]
	if line == nil {
		line = &RouteLine{Role: role, Provider: kind, Model: model, Endpoint: endpoint, Reasoning: reasoning, OffMachine: offMachine(kind, endpoint, model)}
		g[k] = line
	}
	line.Nodes = append(line.Nodes, node)
}

// lines are the groups' lines, each with its nodes sorted, in
// routeLineLess's order.
func (g routeGroups) lines() []RouteLine {
	out := make([]RouteLine, 0, len(g))
	for _, line := range g {
		sort.Strings(line.Nodes)
		out = append(out, *line)
	}
	sort.Slice(out, func(i, j int) bool { return routeLineLess(out[i], out[j]) })
	return out
}

// routeLineLess orders lines by role, then model, then first node.
func routeLineLess(a, b RouteLine) bool {
	if a.Role != b.Role {
		return a.Role < b.Role
	}
	if a.Model != b.Model {
		return a.Model < b.Model
	}
	return a.Nodes[0] < b.Nodes[0]
}

// offMachine says why calls to model on kind at endpoint leave this
// machine, or "" when they stay on it: a CLI backend picks its own host; an
// endpoint that is not loopback is another machine; an Ollama cloud model is
// forwarded to ollama.com from any endpoint. An injected backend (kind "")
// is a test's, and says nothing.
func offMachine(kind BackendKind, endpoint, model string) string {
	switch {
	case isCLIKind(kind):
		return fmt.Sprintf("the %s backend's CLI chooses its own host", kind)
	case ollamaCloudModel(model):
		return fmt.Sprintf("%s is an Ollama cloud model, which Ollama forwards to ollama.com", model)
	case remoteEndpoint(endpoint):
		return endpoint + " is not this machine"
	}
	return ""
}

// isCLIKind reports whether kind is served by a CLI backend.
func isCLIKind(kind BackendKind) bool {
	return kind == BackendClaudeCLI || kind == BackendGeminiCLI
}

// remoteEndpoint reports whether endpoint is set and not on this machine.
func remoteEndpoint(endpoint string) bool {
	return endpoint != "" && !isLoopbackURL(endpoint)
}

// String is the line's routing, for the run log and chb preflight.
func (l RouteLine) String() string {
	who := "role " + l.Role
	if l.Role == "" {
		who = "no role"
	}
	where := ""
	if l.Endpoint != "" {
		where = " at " + l.Endpoint
	}
	return fmt.Sprintf("%s → %s on %s%s, reasoning %s [%s]", who, cmp.Or(l.Model, "the provider's default model"), l.Provider, where, cmp.Or(l.Reasoning, "unset"), strings.Join(l.Nodes, ", "))
}

// RoutingReport is what the run log and chb preflight print for a routing:
// the profile and its quality note when there is one, one line per route,
// then the routes that send data off this machine, with each cloud role's
// because:, or that none does; then what reaches the network without
// calling a model: the model nodes that keep web_fetch or shell, and the
// command nodes.
func RoutingReport(p *Profile, lines []RouteLine, defn map[string]any) []string {
	out := profileHeader(p)
	for _, l := range lines {
		out = append(out, l.String())
	}
	out = append(out, offMachineReport(p, lines)...)
	return append(out, networkReport(defn)...)
}

// profileHeader is the report's line on the profile and its quality note;
// none with no profile.
func profileHeader(p *Profile) []string {
	if p == nil {
		return nil
	}
	return []string{fmt.Sprintf("profile %s (quality: %s)", p.Name, cmp.Or(p.Quality, "not stated"))}
}

// offMachineReport is the report's lines on the routes that send data off
// this machine, each with its role's because: in p when p gives one, or
// the line that none does.
func offMachineReport(p *Profile, lines []RouteLine) []string {
	var out []string
	for _, l := range lines {
		if l.OffMachine != "" {
			out = append(out, "off this machine: "+l.String()+": "+l.OffMachine+routeBecause(p, l.Role))
		}
	}
	if len(out) == 0 {
		return []string{"off this machine: nothing; every model call goes to an endpoint on it"}
	}
	return out
}

// routeBecause is the note on role's because: in p, when p routes role and
// says why; "" otherwise, and with no profile.
func routeBecause(p *Profile, role string) string {
	if p == nil {
		return ""
	}
	if r, ok := p.Routes[role]; ok && r.Because != "" {
		return " (because: " + r.Because + ")"
	}
	return ""
}

// networkReport is the report's lines on what reaches the network without
// calling a model: the model nodes that keep web_fetch or shell, and the
// command nodes.
func networkReport(defn map[string]any) []string {
	var out []string
	if nodes := networkToolNodes(defn); len(nodes) > 0 {
		out = append(out, fmt.Sprintf("network: web_fetch and shell reach the network without calling a model (shell runs any command, curl included); a tool loop may use them in %s", strings.Join(nodes, ", ")))
	}
	if nodes := commandNodes(defn); len(nodes) > 0 {
		out = append(out, fmt.Sprintf("network: command nodes run programs of their own, which may reach the network: %s", strings.Join(nodes, ", ")))
	}
	return out
}

// networkToolNodes are the model nodes that keep web_fetch or shell: every
// tool when they declare no tools:, or a list that names either, shell by
// either of its names. Sorted.
func networkToolNodes(defn map[string]any) []string {
	var out []string
	forEachAgentNode(defn, "", func(name string, node map[string]any, _ BackendKind) {
		raw, has := node["tools"]
		list, _ := raw.([]any)
		if !has || slices.ContainsFunc(list, isNetworkTool) {
			out = append(out, name)
		}
	})
	sort.Strings(out)
	return out
}

// isNetworkTool reports whether a `tools:` entry names web_fetch or shell.
func isNetworkTool(entry any) bool {
	s, _ := entry.(string)
	s = workflow.CanonicalTool(s)
	return s == "web_fetch" || s == "shell"
}

// commandNodes are the workflow's command nodes, sorted.
func commandNodes(defn map[string]any) []string {
	var out []string
	nodes, _ := defn["nodes"].(map[string]any)
	for name, raw := range nodes {
		if node, _ := raw.(map[string]any); node["type"] == "command" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// PreflightLocality refuses a run under a profile when a call would leave
// this machine on a line whose role has no cloud route in the profile. A
// cloud route carries because: (ResolveProfile), so data leaves only for a
// role the profile names evidence for. It refuses a node the profile does
// not route, left on a cloud provider or a CLI; a role routed local whose
// endpoint is not loopback (HIVE_LOCAL_BASE_URL pointed elsewhere); and
// an Ollama cloud model on a node the profile does not route. With no
// profile it refuses nothing.
func PreflightLocality(p *Profile, lines []RouteLine) error {
	if p == nil {
		return nil
	}
	off := p.uncoveredOffMachine(lines)
	if len(off) == 0 {
		return nil
	}
	return fmt.Errorf("routing profile %s sends data off this machine only on a cloud route with because: (%s), so it refuses a run whose calls would leave it here:\n  %s\nroute these nodes' roles to this machine in the profile, or give the role a cloud route with a because: key",
		p.Name, p.cloudRolesNote(), strings.Join(off, "\n  "))
}

// uncoveredOffMachine are the lines, with why, whose calls leave this
// machine on a role p has no cloud route for.
func (p *Profile) uncoveredOffMachine(lines []RouteLine) []string {
	var off []string
	for _, l := range lines {
		if l.OffMachine != "" && !p.cloudRoute(l.Role) {
			off = append(off, l.String()+": "+l.OffMachine)
		}
	}
	return off
}

// cloudRoute reports whether p routes role, which is not "", on a cloud
// route.
func (p *Profile) cloudRoute(role string) bool {
	r, ok := p.Routes[role]
	return ok && role != "" && r.Cloud()
}

// cloudRolesNote says which roles p has cloud routes for.
func (p *Profile) cloudRolesNote() string {
	if roles := p.CloudRoles(); len(roles) > 0 {
		return "it has them for " + strings.Join(roles, ", ")
	}
	return "it has none"
}

// preflightProfileBackends refuses a run under a profile when a node's
// provider cannot serve it: its backend cannot be built, or it is the
// Anthropic SDK and neither ANTHROPIC_API_KEY nor ANTHROPIC_AUTH_TOKEN is
// set, which the SDK backend finds out only on its first call. Under a
// profile a node never falls back to the run default (resolveDispatchParams),
// so such a node would fail mid-run; the run is refused before it starts,
// naming each provider once with its nodes.
func preflightProfileBackends(cfg Config, defn map[string]any) error {
	nodes := agentNodesByKind(cfg, defn)
	var problems []string
	for _, kind := range slices.Sorted(maps.Keys(nodes)) {
		if err := profileBackendError(cfg, kind); err != nil {
			sort.Strings(nodes[kind])
			problems = append(problems, fmt.Sprintf("provider %s cannot serve %s: %v", kind, strings.Join(nodes[kind], ", "), err))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("routing profile %s: under a profile a node runs where it is routed or not at all:\n  %s", cfg.Profile, strings.Join(problems, "\n  "))
	}
	return nil
}

// agentNodesByKind are the nodes of defn that forEachAgentNode serves, by
// the kind that serves them under cfg's run default; a node with no kind is
// left out.
func agentNodesByKind(cfg Config, defn map[string]any) map[BackendKind][]string {
	nodes := map[BackendKind][]string{}
	forEachAgentNode(defn, runDefaultKind(cfg), func(name string, _ map[string]any, kind BackendKind) {
		if kind != "" {
			nodes[kind] = append(nodes[kind], name)
		}
	})
	return nodes
}

// profileBackendError is why kind cannot serve a node under a profile: its
// backend cannot be built from cfg, or it is the Anthropic SDK with no
// credential. nil when it can.
func profileBackendError(cfg Config, kind BackendKind) error {
	nodeCfg := cfg
	nodeCfg.Provider = string(kind)
	if _, err := newRawBackend(kind, nodeCfg); err != nil {
		return err
	}
	if kind == BackendAnthropic && !anthropicCredentialSet(cfg) {
		return fmt.Errorf("the anthropic backend needs ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, and neither is set")
	}
	return nil
}

// anthropicCredentialSet reports whether the Anthropic SDK backend has a
// credential: cfg's key, ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN.
func anthropicCredentialSet(cfg Config) bool {
	return cfg.APIKey != "" || os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != ""
}

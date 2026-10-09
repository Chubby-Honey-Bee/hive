// This module exists to fence the design bench fixtures off the repository's
// module: each task's tree/ is a module of its own, and its hidden/ and
// reference/ files are compiled only when the harness drops them into a
// copy of that tree. Nothing builds this module.
module fixtures.design

go 1.22

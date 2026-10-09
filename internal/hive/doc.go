// Package hive is autonomous mode's control loop (hive.md). Each pass scans
// a project's research state (ScanState), raises the bee-colony signals
// (EvaluateSignals), turns them into a prioritised dispatch plan
// (GeneratePlan), and records the scan and, after the plan has run, the
// pass's completion (RecordScan, CompleteIteration). CheckTermination and
// LoopDecision say when the loop stops; Resolve and Init choose the one
// database each project's hive lives in.
package hive

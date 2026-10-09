// Package workflow is the engine that runs a workflow: a graph of agent,
// decision, parallel_fan, command, calibrate and human_review nodes read from
// YAML. It loads and validates a definition, starts a run, hands out the
// nodes that are ready, completes, fails and retries them, evaluates the
// conditions and accept: predicates that gate them, and settles the run. It
// also renders the run tokens a swarm's evaluator and Queen read, and a swarm
// run's calibration. It stores everything through Store.
package workflow

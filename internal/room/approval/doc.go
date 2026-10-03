// Package approval manages human approval requests, decisions, scope, and TTL.
//
// Approve and public member changes hold the same OS-backed transaction lock
// from membership replay through signing and append. An approval and a role
// change therefore have one consistent order across local processes. The lock
// is confined to ignored .symroom metadata and released on process exit.
//
// Offline/imported journal events may bypass this local lock. Run projection
// separately validates signatures and approval permission at each event's
// position in the total journal order; invalid approvals remain audit records,
// are reported by journal verification, and cannot authorize run.Start.
package approval

package paseoevent

// ReporterBinary is the AO-owned worker binary that moves agent-authored JSON
// onto the deterministic terminal transport. It is a separate program from
// Paseo: workers install it on PATH and AO starts it inside a dedicated
// workspace terminal before launching the agent.
const ReporterBinary = "ao-paseo-reporter"

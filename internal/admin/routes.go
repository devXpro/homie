package admin

// table lists every endpoint of /api/home (docs/ADMIN-API.md §4), relative to basePath.
func (s *Server) table() []route {
	return []route{
		{"GET /bootstrap", levelPublic, s.getBootstrap},
		{"GET /overview", levelOwner, s.getOverview},

		// plugins and tools
		{"GET /plugins", levelOwner, s.listPlugins},
		{"GET /plugins/preview", levelOwner, s.previewPlugin},
		{"POST /plugins/check-updates", levelOwner, s.checkUpdates},
		{"POST /plugins", levelOwnerStepUp, s.installPlugin},
		{"GET /plugins/{id}", levelOwner, s.getPlugin},
		{"PATCH /plugins/{id}", levelOwner, s.patchPlugin},
		{"DELETE /plugins/{id}", levelOwnerStepUp, s.removePlugin},
		{"POST /plugins/{id}/restart", levelOwner, s.restartPlugin},
		{"POST /plugins/{id}/update", levelOwnerStepUp, s.updatePlugin},
		{"GET /plugins/{id}/inventory", levelOwner, s.getInventory},
		{"GET /plugins/{id}/logs", levelOwner, s.pluginLogs},
		{"GET /tools", levelOwner, s.listTools},
		{"POST /tools/{name}/calls", levelOwner, s.startCall},
		{"GET /calls", levelOwner, s.listCalls},
		{"GET /calls/{id}", levelOwner, s.getCall},
		{"DELETE /calls/{id}", levelOwner, s.cancelCall},
		{"GET /jobs", levelOwner, s.listJobs},
		{"GET /jobs/{id}", levelOwner, s.getJob},

		// the activity log: a member sees their own
		{"GET /activity", levelSession, s.listActivity},
		{"GET /activity/{id}", levelSession, s.getActivity},

		// settings, tunnel, system
		{"GET /settings", levelOwner, s.getSettings},
		{"PATCH /settings", levelOwner, s.patchSettings},
		{"GET /tunnel", levelOwner, s.getTunnel},
		{"POST /tunnel/check", levelOwner, s.checkTunnel},
		{"POST /tunnel/reconnect", levelOwnerStepUp, s.reconnectTunnel},
		{"GET /system", levelOwner, s.getSystem},
		{"POST /system/check-update", levelOwner, s.checkHubUpdate},
		{"POST /system/update", levelOwnerStepUp, s.updateHub},

		// events: SSE, and a long poll where SSE does not get through (a quick tunnel)
		{"GET /events", levelSession, s.streamEvents},
		{"GET /events/poll", levelSession, s.pollEvents},
	}
}

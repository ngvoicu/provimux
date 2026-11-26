package pluginhost

// Provimux: a credential a plugin made is named by this gateway's credential, never by fields plugin code wrote
// (.github/provimux/design/answered-by.md 2.3). The account marker recognises it by its storage.

// ProvimuxPluginMade reports that the credential holding this storage was made by a plugin's parser or provider.
func (*pluginTokenStorage) ProvimuxPluginMade() bool { return true }

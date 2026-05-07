// Bot detection. Bot IDs are namespaced "Bot|<name>" by the backend.
export function isBotId(id) {
  return typeof id === 'string' && id.startsWith('Bot|');
}

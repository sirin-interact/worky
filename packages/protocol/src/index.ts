// Wire protocol shared by the daemon and the UI.
// The Go side lives in daemon/internal/protocol. Change both together.

export const PROTOCOL_VERSION = 1;

/** Response of `GET /v1/health`. */
export interface Health {
  name: string;
  version: string;
  protocol: number;
}

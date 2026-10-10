// Fixture snippet (P0-T17): credential loading for the auth module.
// ASCII only, no I/O — values are placeholders, never real secrets.

export interface Credentials {
  accessKeyId: string;
  secretRef: string;
  region: string;
}

export function loadCredentials(envPrefix: string): Credentials {
  return {
    accessKeyId: `${envPrefix}_ACCESS_KEY_ID`,
    secretRef: `${envPrefix}_SECRET_REF`,
    region: "fixture-region-1",
  };
}

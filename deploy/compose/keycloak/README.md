# Keycloak local realm import

Increment I1 adds `realm-kapsora.json` here (realm, BFF confidential client, demo users,
role templates). Keycloak imports every `*.json` in this directory on start when the realm
does not exist yet. Until then Keycloak starts with only the master realm.

-- Up migration (sqlite)
CREATE TABLE network_state (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    installation_id TEXT NOT NULL CHECK (
        length(CAST(installation_id AS BLOB)) BETWEEN 1 AND 128
        AND installation_id = trim(installation_id)
        AND installation_id NOT GLOB '*[^A-Za-z0-9._-]*'
        AND substr(installation_id, 1, 1) GLOB '[A-Za-z0-9]'
        AND substr(installation_id, -1, 1) GLOB '[A-Za-z0-9]'
    ),
    ownership_generation INTEGER NOT NULL CHECK (ownership_generation > 0),
    pool_network TEXT NOT NULL CHECK (
        length(CAST(pool_network AS BLOB)) BETWEEN 7 AND 15
        AND pool_network = trim(pool_network)
        AND pool_network GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND pool_network NOT GLOB '*[^0-9.]*'
        AND length(pool_network) - length(replace(pool_network, '.', '')) = 3
    ),
    pool_prefix_length INTEGER NOT NULL CHECK (pool_prefix_length BETWEEN 8 AND 32),
    dns_suffix TEXT NOT NULL CHECK (dns_suffix = '.test'),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL CHECK (updated_at >= created_at),
    revision INTEGER NOT NULL CHECK (revision > 0)
);

CREATE TABLE network_pool_candidates (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    network_state_id INTEGER NOT NULL REFERENCES network_state(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 1 AND 65535),
    address TEXT NOT NULL CHECK (
        length(CAST(address AS BLOB)) BETWEEN 7 AND 15
        AND address = trim(address)
        AND address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND address NOT GLOB '*[^0-9.]*'
        AND length(address) - length(replace(address, '.', '')) = 3
    ),
    generation INTEGER NOT NULL CHECK (generation > 0),
    UNIQUE (network_state_id, ordinal),
    UNIQUE (address),
    UNIQUE (network_state_id, address)
);

CREATE INDEX network_pool_candidates_order_idx ON network_pool_candidates (network_state_id, ordinal, address);

CREATE TABLE network_setup_evidence (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    network_state_id INTEGER NOT NULL REFERENCES network_state(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    component TEXT NOT NULL CHECK (component IN ('machine_ownership', 'loopback_pool', 'resolver', 'low_ports')),
    evidence TEXT NOT NULL CHECK (length(CAST(evidence AS BLOB)) BETWEEN 1 AND 16384 AND length(trim(evidence)) > 0),
    generation INTEGER NOT NULL CHECK (generation > 0),
    verified_at DATETIME NOT NULL,
    UNIQUE (network_state_id, component)
);

CREATE INDEX network_setup_evidence_order_idx ON network_setup_evidence (network_state_id, component);

CREATE TABLE network_shared_listeners (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    network_state_id INTEGER NOT NULL REFERENCES network_state(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN ('dns', 'http', 'https')),
    mode TEXT NOT NULL CHECK (mode IN ('direct', 'redirect')),
    advertised_address TEXT NOT NULL CHECK (
        length(CAST(advertised_address AS BLOB)) BETWEEN 7 AND 15
        AND advertised_address = trim(advertised_address)
        AND advertised_address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND advertised_address NOT GLOB '*[^0-9.]*'
        AND length(advertised_address) - length(replace(advertised_address, '.', '')) = 3
    ),
    advertised_port INTEGER NOT NULL CHECK (advertised_port BETWEEN 1 AND 65535),
    bind_address TEXT NOT NULL CHECK (
        length(CAST(bind_address AS BLOB)) BETWEEN 7 AND 15
        AND bind_address = trim(bind_address)
        AND bind_address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND bind_address NOT GLOB '*[^0-9.]*'
        AND length(bind_address) - length(replace(bind_address, '.', '')) = 3
    ),
    bind_port INTEGER NOT NULL CHECK (bind_port BETWEEN 1 AND 65535),
    generation INTEGER NOT NULL CHECK (generation > 0),
    verified_at DATETIME NOT NULL,
    UNIQUE (network_state_id, kind),
    UNIQUE (advertised_address, advertised_port),
    UNIQUE (bind_address, bind_port),
    CHECK (
        (mode = 'direct' AND advertised_address = bind_address AND advertised_port = bind_port)
        OR
        (mode = 'redirect' AND advertised_address = bind_address AND advertised_port <> bind_port)
    )
);

CREATE INDEX network_shared_listeners_order_idx ON network_shared_listeners (network_state_id, kind);

CREATE TABLE loopback_address_leases (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    network_state_id INTEGER NOT NULL REFERENCES network_state(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    project_id TEXT REFERENCES projects(project_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    source_project_id TEXT NOT NULL CHECK (length(CAST(source_project_id AS BLOB)) BETWEEN 1 AND 256 AND source_project_id = trim(source_project_id)),
    kind TEXT NOT NULL CHECK (kind IN ('primary', 'secondary')),
    secondary_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(secondary_id AS BLOB)) <= 255 AND secondary_id = trim(secondary_id)),
    address TEXT NOT NULL UNIQUE CHECK (
        length(CAST(address AS BLOB)) BETWEEN 7 AND 15
        AND address = trim(address)
        AND address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND address NOT GLOB '*[^0-9.]*'
        AND length(address) - length(replace(address, '.', '')) = 3
    ),
    state TEXT NOT NULL CHECK (state IN ('leased', 'quarantined')),
    lease_generation INTEGER NOT NULL CHECK (lease_generation > 0),
    ownership_installation_id TEXT NOT NULL CHECK (
        length(CAST(ownership_installation_id AS BLOB)) BETWEEN 1 AND 128
        AND ownership_installation_id = trim(ownership_installation_id)
        AND ownership_installation_id NOT GLOB '*[^A-Za-z0-9._-]*'
        AND substr(ownership_installation_id, 1, 1) GLOB '[A-Za-z0-9]'
        AND substr(ownership_installation_id, -1, 1) GLOB '[A-Za-z0-9]'
    ),
    ownership_generation INTEGER NOT NULL CHECK (ownership_generation > 0),
    ensure_evidence TEXT NOT NULL CHECK (length(CAST(ensure_evidence AS BLOB)) BETWEEN 1 AND 16384 AND length(trim(ensure_evidence)) > 0),
    leased_at DATETIME NOT NULL,
    release_generation INTEGER CHECK (release_generation IS NULL OR release_generation > 0),
    release_evidence TEXT CHECK (release_evidence IS NULL OR (length(CAST(release_evidence AS BLOB)) BETWEEN 1 AND 16384 AND length(trim(release_evidence)) > 0)),
    released_at DATETIME,
    quarantined_at DATETIME,
    reuse_after DATETIME,
    quarantine_reason TEXT CHECK (quarantine_reason IS NULL OR (length(CAST(quarantine_reason AS BLOB)) BETWEEN 1 AND 1024 AND quarantine_reason = trim(quarantine_reason))),
    UNIQUE (id, project_id, address),
    FOREIGN KEY (network_state_id, address)
        REFERENCES network_pool_candidates(network_state_id, address)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CHECK (
        (kind = 'primary' AND secondary_id = '')
        OR
        (kind = 'secondary' AND length(CAST(secondary_id AS BLOB)) BETWEEN 1 AND 255)
    ),
    CHECK (
        (state = 'leased'
            AND project_id IS NOT NULL
            AND project_id = source_project_id
            AND release_generation IS NULL
            AND release_evidence IS NULL
            AND released_at IS NULL
            AND quarantined_at IS NULL
            AND reuse_after IS NULL
            AND quarantine_reason IS NULL)
        OR
        (state = 'quarantined'
            AND project_id IS NULL
            AND release_generation IS NOT NULL
            AND release_generation > lease_generation
            AND release_evidence IS NOT NULL
            AND released_at IS NOT NULL
            AND released_at >= leased_at
            AND quarantined_at IS NOT NULL
            AND quarantined_at >= released_at
            AND reuse_after IS NOT NULL
            AND reuse_after >= quarantined_at
            AND quarantine_reason IS NOT NULL)
    )
);

CREATE INDEX loopback_address_leases_state_idx ON loopback_address_leases (network_state_id, state, address);
CREATE INDEX loopback_address_leases_project_idx ON loopback_address_leases (project_id, kind, secondary_id);
CREATE UNIQUE INDEX loopback_address_leases_active_key_idx
    ON loopback_address_leases (source_project_id, secondary_id)
    WHERE state = 'leased';

CREATE TABLE public_endpoint_leases (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    network_state_id INTEGER NOT NULL REFERENCES network_state(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    project_id TEXT NOT NULL REFERENCES projects(project_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    endpoint_id TEXT NOT NULL CHECK (
        length(CAST(endpoint_id AS BLOB)) BETWEEN 1 AND 128
        AND endpoint_id = trim(endpoint_id)
        AND endpoint_id NOT GLOB '*[^A-Za-z0-9._:-]*'
    ),
    protocol TEXT NOT NULL CHECK (protocol IN ('http', 'tcp')),
    hostname TEXT NOT NULL CHECK (
        length(CAST(hostname AS BLOB)) BETWEEN 6 AND 253
        AND hostname = lower(hostname)
        AND hostname = trim(hostname)
        AND substr(hostname, -5) = '.test'
        AND hostname NOT GLOB '*[^a-z0-9.-]*'
        AND hostname NOT LIKE '.%'
        AND hostname NOT LIKE '%.'
        AND hostname NOT LIKE '%..%'
        AND hostname NOT GLOB '-*'
        AND hostname NOT GLOB '*.-*'
        AND hostname NOT GLOB '*-.*'
    ),
    address TEXT NOT NULL CHECK (
        length(CAST(address AS BLOB)) BETWEEN 7 AND 15
        AND address = trim(address)
        AND address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND address NOT GLOB '*[^0-9.]*'
        AND length(address) - length(replace(address, '.', '')) = 3
    ),
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    loopback_address_lease_id INTEGER,
    generation INTEGER NOT NULL CHECK (generation > 0),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL CHECK (updated_at >= created_at),
    UNIQUE (project_id, endpoint_id),
    UNIQUE (hostname),
    FOREIGN KEY (loopback_address_lease_id, project_id, address)
        REFERENCES loopback_address_leases(id, project_id, address)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CHECK (
        (protocol = 'http' AND loopback_address_lease_id IS NULL)
        OR
        (protocol = 'tcp' AND loopback_address_lease_id IS NOT NULL)
    )
);

CREATE INDEX public_endpoint_leases_project_idx ON public_endpoint_leases (project_id, protocol, endpoint_id);
CREATE INDEX public_endpoint_leases_socket_idx ON public_endpoint_leases (address, port, protocol, hostname);
CREATE UNIQUE INDEX public_endpoint_leases_tcp_socket_idx
    ON public_endpoint_leases (address, port)
    WHERE protocol = 'tcp';

CREATE TABLE network_project_releases (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    network_state_id INTEGER NOT NULL REFERENCES network_state(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    project_id TEXT REFERENCES projects(project_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    source_project_id TEXT NOT NULL CHECK (length(CAST(source_project_id AS BLOB)) BETWEEN 1 AND 256 AND source_project_id = trim(source_project_id)),
    operation_id TEXT NOT NULL REFERENCES operations(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('releasing', 'completed')),
    begin_generation INTEGER NOT NULL CHECK (begin_generation > 0),
    began_at DATETIME NOT NULL,
    completion_generation INTEGER CHECK (completion_generation IS NULL OR completion_generation > 0),
    completed_at DATETIME,
    release_evidence TEXT CHECK (release_evidence IS NULL OR (length(CAST(release_evidence AS BLOB)) BETWEEN 1 AND 16384 AND length(trim(release_evidence)) > 0)),
    UNIQUE (source_project_id),
    UNIQUE (operation_id),
    CHECK (
        (state = 'releasing'
            AND project_id IS NOT NULL
            AND project_id = source_project_id
            AND completion_generation IS NULL
            AND completed_at IS NULL
            AND release_evidence IS NULL)
        OR
        (state = 'completed'
            AND project_id IS NULL
            AND completion_generation IS NOT NULL
            AND completion_generation > begin_generation
            AND completed_at IS NOT NULL
            AND completed_at >= began_at
            AND release_evidence IS NOT NULL)
    )
);

CREATE INDEX network_project_releases_state_idx ON network_project_releases (network_state_id, state, began_at, source_project_id);

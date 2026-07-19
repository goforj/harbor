-- Up migration (sqlite)
CREATE UNIQUE INDEX operations_approval_project_revision_idx
    ON operations (id, project_id, revision);

CREATE UNIQUE INDEX loopback_address_leases_approval_identity_idx
    ON loopback_address_leases (
        id,
        network_state_id,
        project_id,
        kind,
        secondary_id,
        address,
        ownership_installation_id,
        ownership_generation
    );

CREATE TABLE helper_approval_plans (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    operation_id TEXT NOT NULL,
    operation_revision INTEGER NOT NULL CHECK (
        operation_revision BETWEEN 1 AND 9007199254740991
    ),
    network_state_id INTEGER NOT NULL,
    mutation TEXT NOT NULL CHECK (mutation IN ('ensure_loopback_identity', 'release_loopback_identity')),
    lease_state TEXT NOT NULL CHECK (lease_state IN ('pending', 'active')),
    project_id TEXT NOT NULL CHECK (
        length(CAST(project_id AS BLOB)) BETWEEN 1 AND 256
        AND project_id = trim(project_id)
    ),
    kind TEXT NOT NULL CHECK (kind IN ('primary', 'secondary')),
    secondary_id TEXT NOT NULL DEFAULT '' CHECK (
        length(CAST(secondary_id AS BLOB)) <= 255
        AND secondary_id = trim(secondary_id)
    ),
    address TEXT NOT NULL UNIQUE CHECK (
        length(CAST(address AS BLOB)) BETWEEN 7 AND 15
        AND address = trim(address)
        AND address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND address NOT GLOB '*[^0-9.]*'
        AND length(address) - length(replace(address, '.', '')) = 3
    ),
    ownership_installation_id TEXT NOT NULL CHECK (
        length(CAST(ownership_installation_id AS BLOB)) BETWEEN 1 AND 128
        AND ownership_installation_id = trim(ownership_installation_id)
        AND ownership_installation_id NOT GLOB '*[^A-Za-z0-9._-]*'
        AND substr(ownership_installation_id, 1, 1) GLOB '[A-Za-z0-9]'
        AND substr(ownership_installation_id, -1, 1) GLOB '[A-Za-z0-9]'
    ),
    ownership_generation INTEGER NOT NULL CHECK (ownership_generation > 0),
    loopback_address_lease_id INTEGER,
    UNIQUE (operation_id, project_id, secondary_id),
    UNIQUE (project_id, secondary_id),
    FOREIGN KEY (project_id)
        REFERENCES projects(project_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (operation_id, project_id, operation_revision)
        REFERENCES operations(id, project_id, revision)
        ON UPDATE RESTRICT ON DELETE CASCADE,
    FOREIGN KEY (network_state_id)
        REFERENCES network_state(id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (network_state_id, address)
        REFERENCES network_pool_candidates(network_state_id, address)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (
        loopback_address_lease_id,
        network_state_id,
        project_id,
        kind,
        secondary_id,
        address,
        ownership_installation_id,
        ownership_generation
    )
        REFERENCES loopback_address_leases(
            id,
            network_state_id,
            project_id,
            kind,
            secondary_id,
            address,
            ownership_installation_id,
            ownership_generation
        )
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CHECK (
        (kind = 'primary' AND secondary_id = '')
        OR
        (kind = 'secondary' AND length(CAST(secondary_id AS BLOB)) BETWEEN 1 AND 255)
    ),
    CHECK (
        (lease_state = 'pending'
            AND mutation = 'ensure_loopback_identity'
            AND loopback_address_lease_id IS NULL)
        OR
        (lease_state = 'active'
            AND loopback_address_lease_id IS NOT NULL)
    )
);

CREATE INDEX helper_approval_plans_operation_idx
    ON helper_approval_plans (operation_id, operation_revision);

CREATE INDEX helper_approval_plans_lease_idx
    ON helper_approval_plans (network_state_id, project_id, kind, secondary_id, address);

CREATE TABLE helper_approval_plan_socket_requirements (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    helper_approval_plan_id INTEGER NOT NULL
        REFERENCES helper_approval_plans(id)
        ON UPDATE RESTRICT ON DELETE CASCADE,
    transport TEXT NOT NULL CHECK (transport IN ('tcp4', 'udp4')),
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    UNIQUE (helper_approval_plan_id, transport, port)
);

CREATE INDEX helper_approval_plan_socket_requirements_order_idx
    ON helper_approval_plan_socket_requirements (helper_approval_plan_id, transport, port);

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/db/crud/ — CRUD per domain (mirrors sa_core/db/crud/)

from src.db.crud.ue import (
    ue_list, ue_get, ue_add, ue_update, ue_delete,
    ue_clone, ue_import_bulk, ue_count,
)
from src.db.crud.gnb import (
    gnb_list, gnb_get, gnb_add, gnb_update, gnb_delete, gnb_clone,
)
from src.db.crud.results import (
    result_save, result_list, result_get, result_stats,
)
from src.db.crud.sync import (
    sync_mark, sync_status, sync_pending,
)
from src.db.crud.migrate import migrate_from_json
from src.db.crud.infrastructure import (
    infra_get, infra_update,
    amf_list, amf_get, amf_add, amf_update, amf_delete,
    sctp_addr_list, sctp_addr_add, sctp_addr_delete,
    amf_assignment_list, amf_assignment_set, amf_assignment_delete,
    get_interfaces, get_active_tunnels,
)
from src.db.crud.traffic_agents import (
    agent_list, agent_get, agent_get_default, agent_get_by_dnn,
    agent_add, agent_update, agent_delete,
    generate_token, migrate_legacy_traffic_url,
    seed_default_traffic_agent,
)
from src.db.crud.traffic_profiles import (
    FIVE_QI_TO_DSCP, dscp_for_five_qi, tos_for_dscp, profile_resolved_dscp,
    profile_list, profile_get, profile_add, profile_update, profile_delete,
    group_list, group_get, group_add, group_update, group_delete,
    group_set_members, group_add_member, group_remove_member,
    flow_upsert, flow_get, flow_list_for_ue, flow_list_by_dnn, flow_clear_for_ue,
    seed_default_profiles,
)

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/db/schemas/ — DDL per domain (mirrors sa_core/db/schemas/)

from src.db.schemas.provisioning import UE_DDL, GNB_DDL
from src.db.schemas.results import RUNS_DDL, RESULTS_DDL, METRICS_DDL, SCHEDULES_DDL
from src.db.schemas.sync import SYNC_DDL
from src.db.schemas.infrastructure import INFRA_DDL
from src.db.schemas.traffic_agents import TRAFFIC_AGENTS_DDL
from src.db.schemas.traffic_profiles import TRAFFIC_PROFILES_DDL

ALL_DDL = (UE_DDL + GNB_DDL + INFRA_DDL + TRAFFIC_AGENTS_DDL +
           TRAFFIC_PROFILES_DDL +
           RUNS_DDL + RESULTS_DDL + METRICS_DDL + SCHEDULES_DDL + SYNC_DDL)

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/ — FastAPI Routers for SA Tester REST API
#
# Mirrors sa_core's webservice/routes/ pattern.
# Each module is a FastAPI APIRouter handling routes for a specific domain.

def register_routers(app):
    """Register route routers with the FastAPI app.

    All routes currently exist inline in app.py (legacy).
    Routers are ready for migration — when an inline route is removed
    from app.py, enable the corresponding router here.

    To migrate: remove inline route from app.py, uncomment router below.
    """
    # Routes already defined inline in app.py — routers ready but not active
    # from src.routes.provisioning import provisioning_router
    # from src.routes.test_execution import test_execution_router
    # from src.routes.reports import reports_router
    # from src.routes.analysis import analysis_router
    # from src.routes.core_mgmt import core_mgmt_router
    # from src.routes.db_api import db_api_router
    # for r in [provisioning_router, test_execution_router, reports_router,
    #           analysis_router, core_mgmt_router, db_api_router]:
    #     app.include_router(r)

    # NEW routers (no conflicts with app.py inline routes)
    from src.routes.infrastructure import infrastructure_router
    from src.routes.cluster_api import cluster_api_router
    from src.routes.traffic_api import traffic_api_router
    from src.routes.mock_mdf import mock_mdf_router
    from src.routes.runner_api import runner_api_router
    app.include_router(infrastructure_router)
    app.include_router(cluster_api_router)
    app.include_router(traffic_api_router)
    app.include_router(mock_mdf_router)
    app.include_router(runner_api_router)

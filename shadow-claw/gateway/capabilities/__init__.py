"""Personal agent capability modules.

Capabilities sit above the tools/connector layer and provide high-level
intent-driven workflows. They do not own persistence, auth, or telemetry.

Each module exposes a ``register_*_tools(connector)`` function that registers
agent tools against ToolRegistry, closing over the injected connector.
Capabilities must be registered explicitly during bot initialisation after
the desired connector is ready.

Example::

    from capabilities.inbox_capability import register_inbox_tools, StubInboxConnector
    from capabilities.calendar_capability import register_calendar_tools, StubCalendarConnector
    from capabilities.memory_capability import register_memory_tools, MemoryCapability
    from knowledge_vault import KnowledgeVault

    register_inbox_tools(StubInboxConnector())
    register_calendar_tools(StubCalendarConnector())
    register_memory_tools(vault=KnowledgeVault(manager), manager=manager)
"""

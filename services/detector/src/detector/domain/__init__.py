"""The detector's pure domain: books, VWAP, fees, detection and opportunity lifecycles.

Nothing here does IO or imports the generated Protobuf code. Adapters translate the wire
messages into these types, and the composition root injects time through `detector.ports.Clock`.
"""

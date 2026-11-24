import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class PriceLevel(_message.Message):
    __slots__ = ("price", "quantity")
    PRICE_FIELD_NUMBER: _ClassVar[int]
    QUANTITY_FIELD_NUMBER: _ClassVar[int]
    price: str
    quantity: str
    def __init__(self, price: _Optional[str] = ..., quantity: _Optional[str] = ...) -> None: ...

class OrderBook(_message.Message):
    __slots__ = ("exchange", "symbol", "sequence", "depth", "bids", "asks", "exchange_ts", "received_ts", "published_ts")
    EXCHANGE_FIELD_NUMBER: _ClassVar[int]
    SYMBOL_FIELD_NUMBER: _ClassVar[int]
    SEQUENCE_FIELD_NUMBER: _ClassVar[int]
    DEPTH_FIELD_NUMBER: _ClassVar[int]
    BIDS_FIELD_NUMBER: _ClassVar[int]
    ASKS_FIELD_NUMBER: _ClassVar[int]
    EXCHANGE_TS_FIELD_NUMBER: _ClassVar[int]
    RECEIVED_TS_FIELD_NUMBER: _ClassVar[int]
    PUBLISHED_TS_FIELD_NUMBER: _ClassVar[int]
    exchange: str
    symbol: str
    sequence: int
    depth: int
    bids: _containers.RepeatedCompositeFieldContainer[PriceLevel]
    asks: _containers.RepeatedCompositeFieldContainer[PriceLevel]
    exchange_ts: _timestamp_pb2.Timestamp
    received_ts: _timestamp_pb2.Timestamp
    published_ts: _timestamp_pb2.Timestamp
    def __init__(self, exchange: _Optional[str] = ..., symbol: _Optional[str] = ..., sequence: _Optional[int] = ..., depth: _Optional[int] = ..., bids: _Optional[_Iterable[_Union[PriceLevel, _Mapping]]] = ..., asks: _Optional[_Iterable[_Union[PriceLevel, _Mapping]]] = ..., exchange_ts: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., received_ts: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., published_ts: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

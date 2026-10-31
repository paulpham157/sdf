"""Equivalent of Go TestBackpressure_MillionsStableGoroutinesAndRAM for Python.

SDF's MessageHub (sdf_core.agent_messaging) persists every message to PostgreSQL
and is not an in-process streaming bus. This script mirrors the Go bus workload:
bounded buffer capacity=64, 4 producer threads, 1 consumer, 2_000_000 messages,
producers block when full (queue.Queue.put).
"""
from __future__ import annotations

import os
import resource
import threading
import time
from queue import Queue

TOTAL = 2_000_000
CAPACITY = 64
PRODUCERS = 4


def rss_bytes() -> int:
    # Linux: ru_maxrss is kilobytes
    return resource.getrusage(resource.RUSAGE_SELF).ru_maxrss * 1024


def main() -> None:
    q: Queue[bytes] = Queue(maxsize=CAPACITY)
    consumed = 0
    consumed_lock = threading.Lock()
    peak_depth = 0
    peak_depth_lock = threading.Lock()
    peak_threads = threading.active_count()
    stop_monitor = threading.Event()

    def monitor() -> None:
        nonlocal peak_threads
        while not stop_monitor.is_set():
            n = threading.active_count()
            if n > peak_threads:
                peak_threads = n
            d = q.qsize()
            with peak_depth_lock:
                nonlocal peak_depth
                if d > peak_depth:
                    peak_depth = d
            time.sleep(0.0005)

    def consumer() -> None:
        nonlocal consumed
        while True:
            item = q.get()
            if item is None:  # type: ignore[comparison-overlap]
                q.task_done()
                break
            with consumed_lock:
                consumed += 1
            q.task_done()

    mon = threading.Thread(target=monitor, name="monitor", daemon=True)
    mon.start()
    cons = threading.Thread(target=consumer, name="consumer")
    cons.start()

    rss_before = rss_bytes()
    threads_before = threading.active_count()
    t0 = time.perf_counter()

    per = TOTAL // PRODUCERS
    errors: list[BaseException] = []

    def producer() -> None:
        try:
            payload = b"x"
            for _ in range(per):
                q.put(payload)  # blocks when full
        except BaseException as e:  # noqa: BLE001
            errors.append(e)

    producers = [threading.Thread(target=producer, name=f"prod-{i}") for i in range(PRODUCERS)]
    for p in producers:
        p.start()
    for p in producers:
        p.join()

    # sentinel
    q.put(None)  # type: ignore[arg-type]
    cons.join()
    elapsed = time.perf_counter() - t0
    stop_monitor.set()
    mon.join(timeout=1)

    rss_after = rss_bytes()
    threads_after = threading.active_count()
    throughput = TOTAL / elapsed if elapsed > 0 else 0.0

    print("BENCH python_bus_backpressure")
    print(f"messages={TOTAL}")
    print(f"capacity={CAPACITY}")
    print(f"producers={PRODUCERS}")
    print(f"consumed={consumed}")
    print(f"elapsed_sec={elapsed:.6f}")
    print(f"throughput_msgs_per_sec={throughput:.2f}")
    print(f"threads_before={threads_before}")
    print(f"peak_threads={peak_threads}")
    print(f"threads_after={threads_after}")
    print(f"peak_queue_depth={peak_depth}")
    print(f"rss_before_bytes={rss_before}")
    print(f"rss_after_bytes={rss_after}")
    print(f"rss_max_bytes={max(rss_before, rss_after)}")
    print(f"pid={os.getpid()}")
    if errors:
        print(f"errors={errors!r}")
        raise SystemExit(1)
    if consumed != TOTAL:
        raise SystemExit(f"consumed {consumed} != {TOTAL}")


if __name__ == "__main__":
    main()

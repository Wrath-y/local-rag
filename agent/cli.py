"""Interactive CLI for the RAG Agent.

Usage:
    python -m agent.cli                        # new session
    python -m agent.cli --session <uuid>       # resume existing session
"""

import asyncio
import sys

from .loop import run
from .memory import new_session


async def main() -> None:
    session_id: str | None = None

    args = sys.argv[1:]
    if len(args) >= 2 and args[0] == "--session":
        session_id = args[1]
        print(f"Resuming session: {session_id}")
    else:
        session_id = new_session()
        print(f"New session: {session_id}")

    print("Type your message (Ctrl+C or Ctrl+D to exit)\n")

    while True:
        try:
            user_input = input("You: ").strip()
        except (EOFError, KeyboardInterrupt):
            print("\nGoodbye.")
            break

        if not user_input:
            continue

        reply = await run(session_id, user_input)
        print(f"Agent: {reply}\n")


if __name__ == "__main__":
    asyncio.run(main())

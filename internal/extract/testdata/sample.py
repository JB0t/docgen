"""Sample module."""
import os

TOKEN = os.environ.get("SAMPLE_TOKEN")


class Client:
    """Talks to the service."""

    def fetch(self, item_id: int) -> dict:
        """Fetch one item."""
        return {}


def _private():
    pass

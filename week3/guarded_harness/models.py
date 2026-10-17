"""Deterministic offline model plus the explicitly configured real provider."""

import os
from urllib.parse import urlsplit

from smolagents import Model, OpenAIModel
from smolagents.models import ChatMessage, ChatMessageToolCall, ChatMessageToolCallFunction
from smolagents.monitoring import TokenUsage


class FakeModel(Model):
    def __init__(self, actions):
        super().__init__(model_id="deterministic-fake")
        self.actions = iter(actions)

    def generate(self, messages, **kwargs):
        action = next(self.actions, {"id": "exhausted", "name": "final_answer", "arguments": {"answer": "Done"}})
        actions = action if isinstance(action, list) else [action]
        return ChatMessage(role="assistant", tool_calls=[ChatMessageToolCall(
            function=ChatMessageToolCallFunction(name=item["name"], arguments=item["arguments"]),
            id=item["id"], type="function") for item in actions],
            token_usage=TokenUsage(input_tokens=1, output_tokens=1))


def real_model():
    required = ("OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL_NAME")
    if any(not os.environ.get(key) for key in required):
        raise ValueError("Set OPENAI_API_KEY, OPENAI_BASE_URL and OPENAI_MODEL_NAME")
    options = {}
    if urlsplit(os.environ["OPENAI_BASE_URL"]).hostname == "api.deepseek.com":
        # smolagents uses tool_choice=required. DeepSeek only supports it in
        # non-thinking mode: https://api-docs.deepseek.com/api/create-chat-completion/
        options["extra_body"] = {"thinking": {"type": "disabled"}}
    return OpenAIModel(model_id=os.environ["OPENAI_MODEL_NAME"], api_base=os.environ["OPENAI_BASE_URL"],
                       api_key=os.environ["OPENAI_API_KEY"], client_kwargs={"timeout": 60, "max_retries": 0},
                       temperature=0, max_tokens=2000, **options)

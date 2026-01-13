#!/usr/bin/env python3
"""Test script for Live API WebSocket relay."""
import asyncio
import json
import sys
import websockets

async def test_live_api(provider: str):
    """Test the Live API relay with a text message."""
    uri = f"ws://localhost:8317/v1/realtime?key=sk-proxy&provider={provider}"

    print(f"Connecting to {uri}...")

    try:
        async with websockets.connect(uri, ping_interval=None) as ws:
            print("Connected!")

            # Send setup message
            setup_msg = {
                "setup": {
                    "model": "models/gemini-2.5-flash-native-audio-preview",
                    "generationConfig": {
                        "responseModalities": ["TEXT"],  # Request text response for easier testing
                        "speechConfig": {
                            "voiceConfig": {
                                "prebuiltVoiceConfig": {
                                    "voiceName": "Puck"
                                }
                            }
                        }
                    }
                }
            }
            print(f"Sending setup: {json.dumps(setup_msg)[:100]}...")
            await ws.send(json.dumps(setup_msg))

            # Wait for setup confirmation
            print("Waiting for setup confirmation...")
            try:
                response = await asyncio.wait_for(ws.recv(), timeout=10.0)
                print(f"Setup response: {response[:200]}..." if len(response) > 200 else f"Setup response: {response}")
            except asyncio.TimeoutError:
                print("Timeout waiting for setup response")
                return

            # Send a text message (client_content)
            content_msg = {
                "clientContent": {
                    "turns": [{
                        "role": "user",
                        "parts": [{"text": "Hello! Say hi back in one sentence."}]
                    }],
                    "turnComplete": True
                }
            }
            print(f"\nSending message: {json.dumps(content_msg)}")
            await ws.send(json.dumps(content_msg))

            # Receive responses
            print("\nWaiting for responses...")
            for i in range(10):  # Read up to 10 messages
                try:
                    response = await asyncio.wait_for(ws.recv(), timeout=15.0)
                    data = json.loads(response) if response.startswith('{') else response
                    print(f"Response {i+1}: {json.dumps(data, indent=2)[:500]}...")

                    # Check if this is the final response
                    if isinstance(data, dict):
                        if data.get("serverContent", {}).get("turnComplete"):
                            print("\n[Turn complete - AI finished responding]")
                            break
                except asyncio.TimeoutError:
                    print(f"No more responses (timeout after {i+1} messages)")
                    break
                except websockets.exceptions.ConnectionClosed as e:
                    print(f"Connection closed: {e}")
                    break

    except Exception as e:
        print(f"Error: {e}")
        import traceback
        traceback.print_exc()

if __name__ == "__main__":
    provider = sys.argv[1] if len(sys.argv) > 1 else "aistudio-t85xs9vct6abddk5"
    print(f"Testing Live API with provider: {provider}\n")
    asyncio.run(test_live_api(provider))

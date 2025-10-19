import os
import sys
import time
import argparse
from pathlib import Path

from selenium import webdriver
from selenium.webdriver.common.by import By
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from selenium.webdriver.common.action_chains import ActionChains
from selenium.common.exceptions import TimeoutException, ElementClickInterceptedException

try:
    from dotenv import load_dotenv
    load_dotenv()
except Exception:
    pass


def ensure_dir(p: Path):
    p.mkdir(parents=True, exist_ok=True)


def build_chrome_options(session_dir: Path, headless: bool = False) -> Options:
    opts = Options()
    # Persist session/cookies for ChatGPT securely in a dedicated profile directory
    opts.add_argument(f"--user-data-dir={session_dir}")
    opts.add_argument("--profile-directory=Default")
    opts.add_argument("--no-sandbox")
    opts.add_argument("--disable-dev-shm-usage")
    opts.add_argument("--disable-features=VizDisplayCompositor")
    opts.add_argument("--disable-web-security")
    if headless:
        # Headless is fine only if already authenticated
        opts.add_argument("--headless=new")
    return opts


def create_driver(headless: bool = False, session_dir: Path | None = None):
    """Создает локальный WebDriver Chrome с persistent профилем, как в selenium_cleaner."""
    opts = build_chrome_options(session_dir or (Path(os.getcwd()) / ".chrome-session"), headless=headless)
    driver = webdriver.Chrome(options=opts)
    driver.set_script_timeout(20)
    return driver


def safe_get(driver, url: str) -> bool:
    try:
        driver.get(url)
        WebDriverWait(driver, 10).until(lambda d: d.execute_script("return document.readyState") == "complete")
        return True
    except Exception:
        return False


def click_button_robustly(driver, button) -> bool:
    try:
        driver.execute_script("arguments[0].scrollIntoView({behavior:'smooth',block:'center'});", button)
        time.sleep(0.3)
        try:
            ActionChains(driver).move_to_element(button).perform()
            time.sleep(0.2)
        except Exception:
            pass
        try:
            button.click()
            return True
        except ElementClickInterceptedException:
            pass
        except Exception:
            pass
        try:
            ActionChains(driver).move_to_element(button).click().perform()
            return True
        except Exception:
            pass
        try:
            driver.execute_script("arguments[0].click();", button)
            return True
        except Exception:
            pass
        try:
            driver.execute_script(
                "var e=new MouseEvent('click',{bubbles:true,cancelable:true,view:window}); arguments[0].dispatchEvent(e);",
                button,
            )
            return True
        except Exception:
            return False
    except Exception:
        return False


def ensure_auth(driver) -> None:
    # Try Sora, if empty page then force login via ChatGPT
    base = "https://sora.chatgpt.com/"
    safe_get(driver, base)
    time.sleep(2)
    imgs = driver.find_elements(By.TAG_NAME, "img")
    if imgs:
        return
    safe_get(driver, "https://chat.openai.com/")
    # Allow manual login once; session persists
    deadline = time.time() + 120
    while time.time() < deadline:
        time.sleep(3)
        if "ChatGPT" in (driver.title or ""):
            break
    safe_get(driver, base)
    time.sleep(2)


def type_prompt(driver, prompt: str, timeout: int = 20) -> bool:
    try:
        wait = WebDriverWait(driver, timeout)
        textarea = wait.until(
            EC.element_to_be_clickable((By.CSS_SELECTOR, "div.relative textarea[placeholder='Describe your video...']"))
        )
        textarea.clear()
        textarea.send_keys(prompt)
        return True
    except TimeoutException:
        print("Textarea not found", file=sys.stderr)
        return False
    except Exception as e:
        print(f"Failed to type prompt: {e}", file=sys.stderr)
        return False


def click_create(driver, timeout: int = 20) -> bool:
    try:
        wait = WebDriverWait(driver, timeout)
        button = wait.until(
            EC.element_to_be_clickable((By.XPATH, "//button[.//span[text()='Create video']]"))
        )
        return click_button_robustly(driver, button)
    except TimeoutException:
        print("Create video button not found", file=sys.stderr)
        return False
    except Exception as e:
        print(f"Failed to click create: {e}", file=sys.stderr)
        return False


def wait_generation_complete(driver, index: int = 0, timeout: int = 600) -> bool:
    """Waits until spinner inside data-index="index" disappears."""
    selector = f"[data-index='{index}'] svg.h-8.w-8"
    wait = WebDriverWait(driver, timeout)
    try:
        # Wait for spinner to appear (up to 60s), then for it to disappear
        try:
            WebDriverWait(driver, 60).until(EC.presence_of_element_located((By.CSS_SELECTOR, selector)))
        except TimeoutException:
            # If spinner never appears, still try to continue
            pass
        wait.until(EC.invisibility_of_element_located((By.CSS_SELECTOR, selector)))
        return True
    except TimeoutException:
        print("Generation did not finish within timeout", file=sys.stderr)
        return False
    except Exception as e:
        print(f"Error waiting for generation: {e}", file=sys.stderr)
        return False


def build_arg_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description="Sora Selenium Runner")
    p.add_argument("--prompt", type=str, required=True, help="Prompt to send to Sora")
    p.add_argument("--headless", action="store_true", help="Run Chrome in headless mode")
    p.add_argument("--session-dir", type=str, default=None, help="Chrome profile dir (default .chrome-session)")
    return p


def main() -> int:
    args = build_arg_parser().parse_args()

    base_dir = Path(__file__).resolve().parent
    session_dir = Path(args.session_dir or (base_dir / ".chrome-session"))
    ensure_dir(session_dir)

    headless_env = os.getenv("SELENIUM_HEADLESS", "false").lower() == "true"
    headless = args.headless or headless_env

    try:
        driver = create_driver(headless=headless, session_dir=session_dir)
    except Exception as e:
        print(f"Failed to start Chrome: {e}", file=sys.stderr)
        return 1

    try:
        ensure_auth(driver)
        if not type_prompt(driver, args.prompt):
            return 2
        if not click_create(driver):
            return 3
        # Inform Go side that waiting has started (optional stdout marker)
        print("WAITING", flush=True)
        ok = wait_generation_complete(driver, index=0)
        if not ok:
            return 4
        print("DONE", flush=True)
        return 0
    finally:
        try:
            driver.quit()
        except Exception:
            pass


if __name__ == "__main__":
    sys.exit(main())
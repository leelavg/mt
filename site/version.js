(() => {
    const addVersionSelector = () => {
        const version = document.getElementById("mt-version");
        const toolbar = document.querySelector("#mdbook-menu-bar .right-buttons");
        if (!version || !toolbar) return;

        toolbar.append(version);
        version.classList.add("mt-ready");
    };

    const addPlaybackSpeedSelector = () => {
        const video = document.getElementById("mt-demo-video");
        if (!video) return;

        const label = document.createElement("label");
        label.className = "mt-playback-speed";
        label.append("Playback speed ");

        const select = document.createElement("select");
        select.setAttribute("aria-label", "Playback speed");
        for (const speed of [0.5, 0.75, 1, 1.25, 1.5, 2]) {
            const option = document.createElement("option");
            option.value = String(speed);
            option.textContent = `${speed}×`;
            option.selected = speed === 1;
            select.append(option);
        }

        select.addEventListener("change", () => {
            video.playbackRate = Number(select.value);
        });
        label.append(select);
        video.insertAdjacentElement("afterend", label);
    };

    const initialize = () => {
        addVersionSelector();
        addPlaybackSpeedSelector();
    };

    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", initialize, { once: true });
    } else {
        initialize();
    }
})();

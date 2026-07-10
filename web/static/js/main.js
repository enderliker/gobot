document.addEventListener("DOMContentLoaded", () => {
    // 1. Mobile Menu Toggle
    const navToggle = document.getElementById("navToggle");
    const navMenu = document.getElementById("navMenu");

    if (navToggle && navMenu) {
        navToggle.addEventListener("click", () => {
            const isActive = navMenu.classList.toggle("active");
            navToggle.setAttribute("aria-expanded", isActive);
        });

        // Close menu when links are clicked
        navMenu.querySelectorAll(".nav-link").forEach(link => {
            link.addEventListener("click", () => {
                navMenu.classList.remove("active");
                navToggle.setAttribute("aria-expanded", "false");
            });
        });
    }

    // 2. Terminal Typing & Interaction Simulation
    const msgUser = document.getElementById("msgUser");
    const msgBotThinking = document.getElementById("msgBotThinking");
    const msgBotConfirmation = document.getElementById("msgBotConfirmation");
    const msgBotSuccess = document.getElementById("msgBotSuccess");
    const btnDemoConfirm = document.getElementById("btnDemoConfirm");

    if (msgUser && msgBotThinking && msgBotConfirmation && msgBotSuccess) {
        // Run animation sequence
        setTimeout(() => {
            msgBotThinking.classList.remove("hidden");
            
            setTimeout(() => {
                msgBotThinking.classList.add("hidden");
                msgBotConfirmation.classList.remove("hidden");
            }, 1800);
        }, 1500);

        if (btnDemoConfirm) {
            btnDemoConfirm.addEventListener("click", () => {
                btnDemoConfirm.disabled = true;
                btnDemoConfirm.textContent = "Ejecutando...";
                
                setTimeout(() => {
                    msgBotConfirmation.classList.add("hidden");
                    msgBotSuccess.classList.remove("hidden");
                }, 1200);
            });
        }
    }

    // 3. Live Stats Fetch & Graceful Degradation
    const statsSection = document.getElementById("liveStatsSection");
    const serverCountSpan = document.getElementById("serverCount");

    if (statsSection && serverCountSpan) {
        fetch("/api/stats")
            .then(res => {
                if (!res.ok) {
                    throw new Error("Stats service offline or failed");
                }
                return res.json();
            })
            .then(data => {
                if (data.servers && data.servers > 0) {
                    serverCountSpan.textContent = data.servers;
                    statsSection.classList.remove("hidden");
                } else {
                    statsSection.classList.add("hidden");
                }
            })
            .catch(() => {
                // If API fails, section remains hidden as per specification
                statsSection.classList.add("hidden");
            });
    }

    // 4. Scroll Reveal (prefers-reduced-motion aware)
    const prefersReducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (!prefersReducedMotion) {
        const observerOptions = {
            threshold: 0.15,
            rootMargin: "0px 0px -50px 0px"
        };

        const revealObserver = new IntersectionObserver((entries, observer) => {
            entries.forEach(entry => {
                if (entry.isIntersecting) {
                    entry.target.classList.add("revealed");
                    observer.unobserve(entry.target);
                }
            });
        }, observerOptions);

        // Apply reveals
        document.querySelectorAll(".flow-step, .feature-card, .command-item").forEach(el => {
            el.classList.add("reveal-on-scroll");
            revealObserver.observe(el);
        });
    }
});
